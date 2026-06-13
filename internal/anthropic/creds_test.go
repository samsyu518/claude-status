package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"go-gin-claude-status/internal/refresh"
)

func writeCreds(t *testing.T, expiresAt time.Time) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), CredentialsFile)
	content := fmt.Sprintf(`{
  "claudeAiOauth": {
    "accessToken": "old-access",
    "refreshToken": "old-refresh",
    "expiresAt": %d,
    "scopes": ["user:inference", "user:profile"],
    "subscriptionType": "max"
  },
  "somethingElse": {"keep": true}
}`, expiresAt.UnixMilli())
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func tokenServer(t *testing.T, calls *int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*calls++
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode refresh body: %v", err)
		}
		if body["grant_type"] != "refresh_token" || body["refresh_token"] != "old-refresh" || body["client_id"] != ClientID {
			t.Errorf("unexpected refresh body: %v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"access_token": "new-access", "refresh_token": "new-refresh", "expires_in": 3600}`)
	}))
}

func TestTokenRefreshesWhenExpired(t *testing.T) {
	calls := 0
	srv := tokenServer(t, &calls)
	defer srv.Close()

	path := writeCreds(t, time.Now().Add(-time.Hour))
	c := NewClient()
	c.TokenURL = srv.URL
	a, err := LoadAccount("test", path, c, false, refresh.New(0))
	if err != nil {
		t.Fatal(err)
	}
	if got := a.SubscriptionType(); got != "max" {
		t.Errorf("SubscriptionType = %q, want max", got)
	}

	tok, err := a.Token(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tok != "new-access" {
		t.Errorf("Token = %q, want new-access", tok)
	}
	if calls != 1 {
		t.Errorf("refresh calls = %d, want 1", calls)
	}

	// File must be rewritten atomically with the rotated tokens, 0600,
	// preserving fields this program does not understand.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		t.Fatal(err)
	}
	if _, ok := top["somethingElse"]; !ok {
		t.Error("unknown top-level field dropped on save")
	}
	var oauth map[string]any
	if err := json.Unmarshal(top["claudeAiOauth"], &oauth); err != nil {
		t.Fatal(err)
	}
	if oauth["accessToken"] != "new-access" || oauth["refreshToken"] != "new-refresh" {
		t.Errorf("saved tokens = %v / %v", oauth["accessToken"], oauth["refreshToken"])
	}
	if _, ok := oauth["scopes"]; !ok {
		t.Error("scopes dropped on save")
	}
	wantExp := time.Now().Add(time.Hour)
	gotExp := time.UnixMilli(int64(oauth["expiresAt"].(float64)))
	if gotExp.Before(wantExp.Add(-time.Minute)) || gotExp.After(wantExp.Add(time.Minute)) {
		t.Errorf("saved expiresAt = %v, want ~%v", gotExp, wantExp)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("file mode = %o, want 600", perm)
		}
	}

	// A second call must not refresh again (token now valid for ~1h).
	if tok, err = a.Token(context.Background()); err != nil || tok != "new-access" {
		t.Fatalf("second Token = %q, %v", tok, err)
	}
	if calls != 1 {
		t.Errorf("refresh calls after second Token = %d, want 1", calls)
	}
}

// rateLimitedTokenServer always returns 429, optionally with a Retry-After.
func rateLimitedTokenServer(t *testing.T, calls *int, retryAfter string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*calls++
		if retryAfter != "" {
			w.Header().Set("Retry-After", retryAfter)
		}
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"error":{"type":"rate_limit_error","message":"Rate limited."}}`)
	}))
}

// A 429 on the token endpoint must arm a cooldown: a single attempt is made,
// and subsequent Token/Refresh calls return a RateLimitedError without hitting
// the endpoint again, so a manual-refresh storm can't hammer it.
func Test429ArmsCooldown(t *testing.T) {
	calls := 0
	srv := rateLimitedTokenServer(t, &calls, "60")
	defer srv.Close()

	path := writeCreds(t, time.Now().Add(-time.Hour)) // expired
	c := NewClient()
	c.TokenURL = srv.URL
	a, err := LoadAccount("test", path, c, false, refresh.New(0))
	if err != nil {
		t.Fatal(err)
	}

	_, err = a.Token(context.Background())
	var rl *RateLimitedError
	if !errors.As(err, &rl) {
		t.Fatalf("Token err = %v, want *RateLimitedError", err)
	}
	if calls != 1 {
		t.Fatalf("refresh calls = %d, want 1", calls)
	}
	if rl.RetryAfter < 50*time.Second || rl.RetryAfter > 60*time.Second {
		t.Errorf("RetryAfter = %v, want ~60s", rl.RetryAfter)
	}

	// Cooldown active: neither a periodic poll (Token) nor a 401-driven Refresh
	// may touch the endpoint again.
	if _, err = a.Token(context.Background()); !errors.As(err, &rl) {
		t.Errorf("second Token err = %v, want *RateLimitedError", err)
	}
	if _, err = a.Refresh(context.Background(), "old-access"); !errors.As(err, &rl) {
		t.Errorf("Refresh err = %v, want *RateLimitedError", err)
	}
	if calls != 1 {
		t.Errorf("refresh calls after cooldown = %d, want 1 (no re-hits)", calls)
	}
}

// When the proactive refresh fails but the token is still valid for the rest of
// the skew window, Token must serve the existing token rather than fail.
func TestProactiveRefreshFailureFallsBackToValidToken(t *testing.T) {
	calls := 0
	srv := rateLimitedTokenServer(t, &calls, "")
	defer srv.Close()

	// Inside the 5m skew but not yet expired.
	path := writeCreds(t, time.Now().Add(2*time.Minute))
	c := NewClient()
	c.TokenURL = srv.URL
	a, err := LoadAccount("test", path, c, false, refresh.New(0))
	if err != nil {
		t.Fatal(err)
	}
	tok, err := a.Token(context.Background())
	if err != nil || tok != "old-access" {
		t.Fatalf("Token = %q, %v; want old-access serving through the 429", tok, err)
	}
	if calls != 1 {
		t.Errorf("refresh calls = %d, want 1", calls)
	}
}

// The refresh request must use the axios User-Agent the real CLI sends and must
// NOT carry the claude-code UA / anthropic-beta header: the token endpoint 429s
// those (the opposite of the usage endpoint).
func TestRefreshSendsAxiosUA(t *testing.T) {
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"access_token": "new-access", "refresh_token": "new-refresh", "expires_in": 3600}`)
	}))
	defer srv.Close()

	path := writeCreds(t, time.Now().Add(-time.Hour))
	c := NewClient()
	c.TokenURL = srv.URL
	a, err := LoadAccount("test", path, c, false, refresh.New(0))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Token(context.Background()); err != nil {
		t.Fatal(err)
	}
	if h := got.Get("User-Agent"); h != refreshUserAgent {
		t.Errorf("User-Agent = %q, want %q", h, refreshUserAgent)
	}
	if h := got.Get("anthropic-beta"); h != "" {
		t.Errorf("anthropic-beta = %q, want empty (token endpoint 429s it)", h)
	}
}

func TestTokenValidSkipsRefresh(t *testing.T) {
	calls := 0
	srv := tokenServer(t, &calls)
	defer srv.Close()

	path := writeCreds(t, time.Now().Add(time.Hour))
	c := NewClient()
	c.TokenURL = srv.URL
	a, err := LoadAccount("test", path, c, false, refresh.New(0))
	if err != nil {
		t.Fatal(err)
	}
	tok, err := a.Token(context.Background())
	if err != nil || tok != "old-access" {
		t.Fatalf("Token = %q, %v", tok, err)
	}
	if calls != 0 {
		t.Errorf("refresh calls = %d, want 0", calls)
	}
}

func TestNoRefreshNeverRefreshes(t *testing.T) {
	path := writeCreds(t, time.Now().Add(-time.Hour))
	c := NewClient()
	c.TokenURL = "http://127.0.0.1:1" // any request would fail loudly
	a, err := LoadAccount("test", path, c, true, refresh.New(0))
	if err != nil {
		t.Fatal(err)
	}
	tok, err := a.Token(context.Background())
	if err != nil || tok != "old-access" {
		t.Fatalf("Token = %q, %v", tok, err)
	}
	if _, err := a.Refresh(context.Background(), "old-access"); err == nil {
		t.Error("Refresh with --no-refresh should error")
	}
}

func TestRefreshSkipsWhenAlreadyRotated(t *testing.T) {
	calls := 0
	srv := tokenServer(t, &calls)
	defer srv.Close()

	path := writeCreds(t, time.Now().Add(time.Hour))
	c := NewClient()
	c.TokenURL = srv.URL
	a, err := LoadAccount("test", path, c, false, refresh.New(0))
	if err != nil {
		t.Fatal(err)
	}
	// A caller holding a stale token must not trigger another rotation.
	tok, err := a.Refresh(context.Background(), "some-older-token")
	if err != nil || tok != "old-access" {
		t.Fatalf("Refresh = %q, %v", tok, err)
	}
	if calls != 0 {
		t.Errorf("refresh calls = %d, want 0", calls)
	}
}
