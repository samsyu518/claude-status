package tui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go-gin-claude-status/internal/store"
)

func TestViewPending(t *testing.T) {
	snaps := []store.Snapshot{{Name: "personal"}} // FetchedAt zero → pending
	m := model{provider: func() []store.Snapshot { return snaps }, snaps: snaps}
	out := m.View()
	if !strings.Contains(out, "personal") {
		t.Errorf("missing account name in:\n%s", out)
	}
	if !strings.Contains(out, "waiting for first fetch") {
		t.Errorf("pending account should show waiting line, got:\n%s", out)
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
	// nil windows are skipped, not rendered.
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
	out := renderAccount(s)
	if !strings.Contains(out, "showing last good data") {
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

func TestRemoteProvider(t *testing.T) {
	const body = `[
	  {
	    "name": "personal",
	    "subscriptionType": "max",
	    "fiveHour":       {"utilization": 33, "resetsAt": "2026-06-12T07:00:00Z"},
	    "sevenDay":       {"utilization": 13, "resetsAt": "2026-06-17T00:59:59Z"},
	    "sevenDaySonnet": {"utilization": 1,  "resetsAt": "2026-06-16T03:00:00Z"},
	    "fetchedAt": "2026-06-12T01:23:45+08:00"
	  }
	]`
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/usage" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		hits++
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body))
	}))
	defer srv.Close()

	provider, err := NewRemoteProvider(t.Context(), srv.URL, time.Hour)
	if err != nil {
		t.Fatalf("NewRemoteProvider: %v", err)
	}
	snaps := provider()
	if len(snaps) != 1 || snaps[0].Name != "personal" {
		t.Fatalf("unexpected snapshots: %+v", snaps)
	}
	if snaps[0].FiveHour == nil || snaps[0].FiveHour.Utilization != 33 {
		t.Errorf("fiveHour not parsed: %+v", snaps[0].FiveHour)
	}
	if hits == 0 {
		t.Error("expected at least one synchronous fetch")
	}
}

func TestRemoteProviderConnError(t *testing.T) {
	if _, err := NewRemoteProvider(t.Context(), "http://127.0.0.1:1", time.Hour); err == nil {
		t.Error("expected error connecting to a dead address")
	}
}
