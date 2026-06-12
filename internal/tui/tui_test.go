package tui

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go-gin-claude-status/internal/lock"
	"go-gin-claude-status/internal/store"
)

type fakeBackend struct {
	snaps []store.Snapshot
	mode  string
}

func (f fakeBackend) Snapshots() []store.Snapshot { return f.snaps }
func (f fakeBackend) Mode() string                { return f.mode }

// staticHost is a HostFunc that doesn't actually serve (it closes the listener)
// and just exposes fixed snapshots — enough to test the role transitions
// without touching real credentials.
func staticHost(snaps []store.Snapshot) HostFunc {
	return func(ctx context.Context, ln net.Listener) (func() []store.Snapshot, error) {
		ln.Close()
		return func() []store.Snapshot { return snaps }, nil
	}
}

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

func TestViewPending(t *testing.T) {
	snaps := []store.Snapshot{{Name: "personal"}} // FetchedAt zero → pending
	m := model{backend: fakeBackend{snaps: snaps, mode: "host"}, snaps: snaps}
	out := m.View()
	for _, want := range []string{"personal", "waiting for first fetch", "[host]"} {
		if !strings.Contains(out, want) {
			t.Errorf("view missing %q in:\n%s", want, out)
		}
	}
}

func TestRenderAccount(t *testing.T) {
	reset := time.Now().Add(90 * time.Minute)
	s := store.Snapshot{
		Name:             "work",
		SubscriptionType: "max",
		FiveHour:         &store.Window{Utilization: 33, ResetsAt: reset},
		SevenDay:         &store.Window{Utilization: 85, ResetsAt: reset.Add(48 * time.Hour)},
		FetchedAt:        time.Now(),
	}
	out := renderAccount(s)
	for _, want := range []string{"work", "[max]", "5h", "33%", "7d", "85%", "1h 30m", "updated"} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered account missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "7d Opus") {
		t.Errorf("nil window should be skipped, got:\n%s", out)
	}
}

func TestRenderAccountError(t *testing.T) {
	s := store.Snapshot{
		Name:      "work",
		FiveHour:  &store.Window{Utilization: 10, ResetsAt: time.Now().Add(time.Hour)},
		FetchedAt: time.Now(),
		Error:     "boom",
	}
	if out := renderAccount(s); !strings.Contains(out, "showing last good data") {
		t.Errorf("error with prior data should note stale data, got:\n%s", out)
	}
}

func TestRenderBarBounds(t *testing.T) {
	if got := strings.Count(renderBar(0), "█"); got != 0 {
		t.Errorf("0%% bar should have no filled cells, got %d", got)
	}
	if got := strings.Count(renderBar(100), "█"); got != barWidth {
		t.Errorf("100%% bar should be full (%d), got %d", barWidth, got)
	}
	if got := strings.Count(renderBar(150), "█"); got != barWidth {
		t.Errorf("over-100%% bar should clamp to %d, got %d", barWidth, got)
	}
}

// TestConnectHosts: a free lock ⇒ this process wins the backend role.
func TestConnectHosts(t *testing.T) {
	want := []store.Snapshot{{Name: "personal"}}
	cfg := Config{
		BindAddr: freeAddr(t),
		LockPath: filepath.Join(t.TempDir(), "x.lock"),
		Host:     staticHost(want),
		Poll:     20 * time.Millisecond,
	}
	s, err := Connect(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if s.Mode() != "host" {
		t.Fatalf("want host, got %q", s.Mode())
	}
	if got := s.Snapshots(); len(got) != 1 || got[0].Name != "personal" {
		t.Fatalf("host snapshots: %+v", got)
	}
}

// TestConnectClientGuard: a backend already holds the lock at addr A. Even when
// this instance was (mistakenly) pointed at a different --listen (B), it must
// attach to A as a client — never spin up a second backend.
func TestConnectClientGuard(t *testing.T) {
	const body = `[{"name":"personal","subscriptionType":"max",
	  "fiveHour":{"utilization":33,"resetsAt":"2026-06-12T07:00:00Z"}}]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/usage" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		w.Write([]byte(body))
	}))
	defer srv.Close()
	addrA := srv.Listener.Addr().String()

	path := filepath.Join(t.TempDir(), "x.lock")
	held, ok, err := lock.Acquire(path) // pretend an existing backend
	if err != nil || !ok {
		t.Fatalf("setup acquire: ok=%v err=%v", ok, err)
	}
	defer held.Release()
	if err := held.WriteInfo(lock.Info{Addr: addrA, PID: 4242}); err != nil {
		t.Fatal(err)
	}

	cfg := Config{
		BindAddr: freeAddr(t), // B ≠ A
		LockPath: path,
		Host:     staticHost(nil),
		Poll:     time.Hour,
	}
	s, err := Connect(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if s.Mode() != "client" {
		t.Fatalf("want client, got %q", s.Mode())
	}
	got := s.Snapshots()
	if len(got) != 1 || got[0].FiveHour == nil || got[0].FiveHour.Utilization != 33 {
		t.Fatalf("client snapshots: %+v", got)
	}
}

// TestFailover: the hosting process dies (server stops, lock released); a
// surviving client must detect the failure and promote itself to host.
func TestFailover(t *testing.T) {
	const body = `[{"name":"x","fiveHour":{"utilization":10,"resetsAt":"2030-01-01T00:00:00Z"},
	  "fetchedAt":"2026-06-12T00:00:00Z"}]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(body))
	}))
	addrA := srv.Listener.Addr().String()

	path := filepath.Join(t.TempDir(), "x.lock")
	held, ok, _ := lock.Acquire(path)
	if !ok {
		t.Fatal("setup lock")
	}
	if err := held.WriteInfo(lock.Info{Addr: addrA, PID: 1}); err != nil {
		t.Fatal(err)
	}

	want := []store.Snapshot{{Name: "promoted"}}
	s, err := Connect(t.Context(), Config{
		BindAddr: freeAddr(t),
		LockPath: path,
		Host:     staticHost(want),
		Poll:     20 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if s.Mode() != "client" {
		t.Fatalf("want client initially, got %q", s.Mode())
	}

	srv.Close()    // host's server gone
	held.Release() // host's lock freed

	deadline := time.Now().Add(3 * time.Second)
	for s.Mode() != "host" {
		if time.Now().After(deadline) {
			t.Fatalf("client did not promote; mode=%q", s.Mode())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := s.Snapshots(); len(got) != 1 || got[0].Name != "promoted" {
		t.Fatalf("after promotion: %+v", got)
	}
}

func TestConnectRemoteError(t *testing.T) {
	if _, err := Connect(t.Context(), Config{URL: "http://127.0.0.1:1/api/usage"}); err == nil {
		t.Error("expected error connecting to a dead address")
	}
}
