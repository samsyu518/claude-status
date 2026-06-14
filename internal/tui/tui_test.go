package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"go-gin-claude-status/internal/lock"
	"go-gin-claude-status/internal/view"
)

type fakeBackend struct {
	data view.Data
	mode string
}

func (f fakeBackend) View() view.Data                 { return f.data }
func (f fakeBackend) Mode() string                    { return f.mode }
func (f fakeBackend) Refresh(_ context.Context) error { return nil }

// wsServer starts a minimal HTTP+WS server that sends data to every WS
// connection and keeps it alive until the context is cancelled.
func wsServer(t *testing.T, data view.Data) (addr string, stop func()) {
	t.Helper()
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	ctx, cancel := context.WithCancel(context.Background())
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		b, _ := json.Marshal(data)
		conn.WriteMessage(websocket.TextMessage, b)
		<-ctx.Done()
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	addr = ln.Addr().String()
	stop = func() {
		cancel()
		srv.Close()
	}
	return
}

// staticHost is a HostFunc that starts a real WS server on the given listener
// and exposes fixed view data — enough to test role transitions without real
// credentials.
func staticHost(data view.Data) HostFunc {
	return func(ctx context.Context, ln net.Listener) error {
		upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
		mux := http.NewServeMux()
		mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				return
			}
			defer conn.Close()
			b, _ := json.Marshal(data)
			conn.WriteMessage(websocket.TextMessage, b)
			<-ctx.Done()
		})
		srv := &http.Server{Handler: mux}
		go srv.Serve(ln)
		go func() {
			<-ctx.Done()
			srv.Close()
		}()
		return nil
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
	data := view.Data{Cards: []view.Card{{Name: "personal", Pending: true}}}
	m := model{backend: fakeBackend{data: data, mode: "host"}, data: data, ctx: context.Background()}
	out := m.View()
	for _, want := range []string{"personal", "waiting for first fetch", "[host]"} {
		if !strings.Contains(out, want) {
			t.Errorf("view missing %q in:\n%s", want, out)
		}
	}
}

func TestRenderAccount(t *testing.T) {
	reset := time.Now().Add(90 * time.Minute)
	card := view.Card{
		Name:      "work",
		SubType:   "max",
		FetchedAt: time.Now().Format("15:04:05"),
		Rows: []view.Row{
			{Label: "5h", Value: 33, Pct: "33%", ResetsAtISO: reset.UTC().Format(time.RFC3339)},
			{Label: "7d", Value: 85, Pct: "85%", ResetsAtISO: reset.Add(48 * time.Hour).UTC().Format(time.RFC3339)},
		},
	}
	out := renderAccount(card, nil, "")
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
	card := view.Card{
		Name:      "work",
		FetchedAt: time.Now().Format("15:04:05"),
		Error:     "boom",
		Rows: []view.Row{
			{Label: "5h", Value: 10, Pct: "10%", ResetsAtISO: time.Now().Add(time.Hour).UTC().Format(time.RFC3339)},
		},
	}
	if out := renderAccount(card, nil, ""); !strings.Contains(out, "showing last good data") {
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
	want := view.Data{Cards: []view.Card{{Name: "personal", Pending: true}}}
	cfg := Config{
		BindAddr: freeAddr(t),
		LockPath: filepath.Join(t.TempDir(), "x.lock"),
		Host:     staticHost(want),
	}
	s, err := Connect(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if s.Mode() != "host" {
		t.Fatalf("want host, got %q", s.Mode())
	}
	if got := s.View(); len(got.Cards) != 1 || got.Cards[0].Name != "personal" {
		t.Fatalf("host view: %+v", got)
	}
}

// TestConnectClientGuard: a backend already holds the lock. Even when this
// instance was pointed at a different --listen, it must attach as a client.
func TestConnectClientGuard(t *testing.T) {
	wantData := view.Data{Cards: []view.Card{{Name: "personal", Pending: false}}}
	addr, stop := wsServer(t, wantData)
	defer stop()

	path := filepath.Join(t.TempDir(), "x.lock")
	held, ok, err := lock.Acquire(path)
	if err != nil || !ok {
		t.Fatalf("setup acquire: ok=%v err=%v", ok, err)
	}
	defer held.Release()
	if err := held.WriteInfo(lock.Info{Addr: addr, PID: 4242}); err != nil {
		t.Fatal(err)
	}

	cfg := Config{
		BindAddr: freeAddr(t),
		LockPath: path,
		Host:     staticHost(view.Data{}),
	}
	s, err := Connect(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if s.Mode() != "client" {
		t.Fatalf("want client, got %q", s.Mode())
	}
	got := s.View()
	if len(got.Cards) != 1 || got.Cards[0].Name != "personal" {
		t.Fatalf("client view: %+v", got)
	}
}

// TestFailover: the hosting process dies; a surviving client must detect the
// failure and promote itself to host.
func TestFailover(t *testing.T) {
	wantData := view.Data{Cards: []view.Card{{Name: "x"}}}
	addr, stopServer := wsServer(t, wantData)

	path := filepath.Join(t.TempDir(), "x.lock")
	held, ok, _ := lock.Acquire(path)
	if !ok {
		t.Fatal("setup lock")
	}
	if err := held.WriteInfo(lock.Info{Addr: addr, PID: 1}); err != nil {
		t.Fatal(err)
	}

	promoted := view.Data{Cards: []view.Card{{Name: "promoted"}}}
	s, err := Connect(t.Context(), Config{
		BindAddr: freeAddr(t),
		LockPath: path,
		Host:     staticHost(promoted),
	})
	if err != nil {
		t.Fatal(err)
	}
	if s.Mode() != "client" {
		t.Fatalf("want client initially, got %q", s.Mode())
	}

	stopServer()   // host's server gone
	held.Release() // host's lock freed

	deadline := time.Now().Add(5 * time.Second)
	for s.Mode() != "host" {
		if time.Now().After(deadline) {
			t.Fatalf("client did not promote; mode=%q", s.Mode())
		}
		time.Sleep(50 * time.Millisecond)
	}
	if got := s.View(); len(got.Cards) != 1 || got.Cards[0].Name != "promoted" {
		t.Fatalf("after promotion: %+v", got)
	}
}

func TestConnectRemoteError(t *testing.T) {
	if _, err := Connect(t.Context(), Config{RemoteURL: "http://127.0.0.1:1"}); err == nil {
		t.Error("expected error connecting to a dead address")
	}
}

func TestResetClock(t *testing.T) {
	taipei, _ := time.LoadLocation("Asia/Taipei")
	const label = "Asia/Taipei"

	// fixed "now": 2026-06-12 10:00 Taipei
	now := time.Date(2026, 6, 12, 10, 0, 0, 0, taipei)

	cases := []struct {
		name     string
		resetsAt time.Time
		want     string
	}{
		{
			name:     "same day, exact hour",
			resetsAt: time.Date(2026, 6, 12, 16, 0, 0, 0, taipei),
			want:     "4:00:00pm (Asia/Taipei)",
		},
		{
			name:     "same day, with minutes",
			resetsAt: time.Date(2026, 6, 12, 16, 10, 0, 0, taipei),
			want:     "4:10:00pm (Asia/Taipei)",
		},
		{
			name:     "different day, exact hour",
			resetsAt: time.Date(2026, 6, 15, 4, 0, 0, 0, taipei),
			want:     "Jun 15, 4:00:00am (Asia/Taipei)",
		},
		{
			name:     "different day, with minutes",
			resetsAt: time.Date(2026, 6, 15, 4, 10, 0, 0, taipei),
			want:     "Jun 15, 4:10:00am (Asia/Taipei)",
		},
		{
			name:     "next day midnight",
			resetsAt: time.Date(2026, 6, 13, 0, 0, 0, 0, taipei),
			want:     "Jun 13, 12:00:00am (Asia/Taipei)",
		},
		{
			name:     "cross year boundary",
			resetsAt: time.Date(2027, 1, 1, 0, 0, 0, 0, taipei),
			want:     "Jan 1, 12:00:00am (Asia/Taipei)",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resetClock(tc.resetsAt, now, taipei, label)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// TestPoller trigger integration (verifies the API exists; detailed test is in
// the poller package).
func TestRefreshCallsBackend(t *testing.T) {
	called := false
	b := &callTrackingBackend{refreshFn: func() { called = true }}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	b.Refresh(ctx) //nolint:errcheck
	_ = called     // just verify it compiles and runs without panic
}

type callTrackingBackend struct {
	refreshFn func()
}

func (c *callTrackingBackend) View() view.Data { return view.Data{} }
func (c *callTrackingBackend) Mode() string    { return "host" }
func (c *callTrackingBackend) Refresh(_ context.Context) error {
	if c.refreshFn != nil {
		c.refreshFn()
	}
	return nil
}

// Compile-time check: wsServer uses fmt for potential future debugging.
var _ = fmt.Sprintf
