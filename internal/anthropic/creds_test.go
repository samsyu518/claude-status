package anthropic

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
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
	a, err := LoadAccount("test", path, c, false)
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

func TestTokenValidSkipsRefresh(t *testing.T) {
	calls := 0
	srv := tokenServer(t, &calls)
	defer srv.Close()

	path := writeCreds(t, time.Now().Add(time.Hour))
	c := NewClient()
	c.TokenURL = srv.URL
	a, err := LoadAccount("test", path, c, false)
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
	a, err := LoadAccount("test", path, c, true)
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
	a, err := LoadAccount("test", path, c, false)
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
