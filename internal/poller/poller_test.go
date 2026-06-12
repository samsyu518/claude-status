package poller

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go-gin-claude-status/internal/anthropic"
	"go-gin-claude-status/internal/store"
)

// End-to-end through real Account + Client: a 401 must trigger one refresh
// and a retry with the rotated token.
func TestPollRefreshesOn401(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/usage", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer new-access" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"five_hour": {"utilization": 50.0, "resets_at": "2099-01-01T00:00:00+00:00"}}`)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"access_token": "new-access", "refresh_token": "new-refresh", "expires_in": 3600}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	path := filepath.Join(t.TempDir(), anthropic.CredentialsFile)
	// expiresAt far in the future so the 401 path (not proactive refresh) is hit.
	creds := fmt.Sprintf(`{"claudeAiOauth": {"accessToken": "old-access", "refreshToken": "r", "expiresAt": %d}}`,
		time.Now().Add(time.Hour).UnixMilli())
	if err := os.WriteFile(path, []byte(creds), 0o600); err != nil {
		t.Fatal(err)
	}

	client := anthropic.NewClient()
	client.UsageURL = srv.URL + "/usage"
	client.TokenURL = srv.URL + "/token"
	acc, err := anthropic.LoadAccount("test", path, client, false)
	if err != nil {
		t.Fatal(err)
	}

	st := store.New([]string{"test"})
	p := &Poller{Account: acc, Client: client, Store: st, Interval: MinInterval}
	if err := p.poll(context.Background()); err != nil {
		t.Fatal(err)
	}

	snaps := st.Snapshots()
	if len(snaps) != 1 {
		t.Fatalf("snapshots = %d", len(snaps))
	}
	s := snaps[0]
	if s.Error != "" {
		t.Errorf("Error = %q", s.Error)
	}
	if s.FiveHour == nil || s.FiveHour.Utilization != 50.0 {
		t.Errorf("FiveHour = %+v", s.FiveHour)
	}
	if s.FetchedAt.IsZero() {
		t.Error("FetchedAt not set")
	}
}
