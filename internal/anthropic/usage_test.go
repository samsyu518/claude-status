package anthropic

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// sampleUsage mirrors the real /api/oauth/usage shape as of 2026-07: the old
// seven_day_opus/seven_day_sonnet fields are always null now, and per-model
// weekly windows live in "limits" instead, one entry per model actually
// tracked this period (Opus here has a null resets_at — never used, so it
// carries no window yet).
const sampleUsage = `{
  "five_hour":        {"utilization": 33.0, "resets_at": "2026-06-12T07:00:00+00:00"},
  "seven_day":        {"utilization": 13.0, "resets_at": "2026-06-17T00:59:59+00:00"},
  "seven_day_opus":   null,
  "seven_day_sonnet": null,
  "limits": [
    {"kind": "session",       "percent": 33, "resets_at": "2026-06-12T07:00:00+00:00", "is_active": true},
    {"kind": "weekly_all",    "percent": 13, "resets_at": "2026-06-17T00:59:59+00:00", "is_active": false},
    {"kind": "weekly_scoped", "percent": 1,  "resets_at": "2026-06-16T03:00:00+00:00", "is_active": false, "scope": {"model": {"display_name": "Sonnet"}}},
    {"kind": "weekly_scoped", "percent": 5,  "resets_at": "2026-06-16T05:00:00+00:00", "is_active": false, "scope": {"model": {"display_name": "Fable"}}},
    {"kind": "weekly_scoped", "percent": 0,  "resets_at": null, "is_active": false, "scope": {"model": {"display_name": "Opus"}}}
  ],
  "extra_usage":      {"is_enabled": false, "monthly_limit": null, "used_credits": null, "utilization": null}
}`

func TestFetchUsage(t *testing.T) {
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, sampleUsage)
	}))
	defer srv.Close()

	c := NewClient()
	c.UsageURL = srv.URL
	u, err := c.FetchUsage(context.Background(), "tok-123")
	if err != nil {
		t.Fatal(err)
	}

	if h := got.Get("Authorization"); h != "Bearer tok-123" {
		t.Errorf("Authorization = %q", h)
	}
	if h := got.Get("User-Agent"); h != UserAgent {
		t.Errorf("User-Agent = %q, want %q", h, UserAgent)
	}
	if h := got.Get("anthropic-beta"); h != betaHeader {
		t.Errorf("anthropic-beta = %q, want %q", h, betaHeader)
	}

	if u.FiveHour == nil || u.FiveHour.Utilization != 33.0 {
		t.Errorf("FiveHour = %+v", u.FiveHour)
	}
	wantReset := time.Date(2026, 6, 12, 7, 0, 0, 0, time.UTC)
	if !u.FiveHour.ResetsAt.Equal(wantReset) {
		t.Errorf("FiveHour.ResetsAt = %v, want %v", u.FiveHour.ResetsAt, wantReset)
	}
	if u.SevenDay == nil || u.SevenDay.Utilization != 13.0 {
		t.Errorf("SevenDay = %+v", u.SevenDay)
	}
	if len(u.Limits) != 5 {
		t.Fatalf("Limits = %d entries, want 5", len(u.Limits))
	}
	var sonnet, fable, opus *Limit
	for i := range u.Limits {
		switch u.Limits[i].ModelName() {
		case "Sonnet":
			sonnet = &u.Limits[i]
		case "Fable":
			fable = &u.Limits[i]
		case "Opus":
			opus = &u.Limits[i]
		}
	}
	if sonnet == nil || sonnet.Percent != 1 || sonnet.ResetsAt.IsZero() {
		t.Errorf("Sonnet limit = %+v", sonnet)
	}
	if fable == nil || fable.Percent != 5 || fable.ResetsAt.IsZero() {
		t.Errorf("Fable limit = %+v", fable)
	}
	if opus == nil || !opus.ResetsAt.IsZero() {
		t.Errorf("Opus limit = %+v, want zero ResetsAt (unused this period)", opus)
	}
	if u.ExtraUsage == nil || u.ExtraUsage.IsEnabled {
		t.Errorf("ExtraUsage = %+v", u.ExtraUsage)
	}
}

func TestFetchUsageUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := NewClient()
	c.UsageURL = srv.URL
	if _, err := c.FetchUsage(context.Background(), "bad"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
}

func TestFetchUsageRateLimited(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	c := NewClient()
	c.UsageURL = srv.URL
	_, err := c.FetchUsage(context.Background(), "tok")
	var rl *RateLimitedError
	if !errors.As(err, &rl) {
		t.Fatalf("err = %v, want RateLimitedError", err)
	}
	if rl.RetryAfter != 60*time.Second {
		t.Errorf("RetryAfter = %v, want 60s", rl.RetryAfter)
	}
}
