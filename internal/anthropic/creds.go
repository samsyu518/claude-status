package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"go-gin-claude-status/internal/refresh"
)

// CredentialsFile is the well-known filename Claude Code writes inside its
// config directory.
const CredentialsFile = ".credentials.json"

const refreshSkew = 5 * time.Minute

// defaultRefreshCooldown is how long to avoid the token endpoint after it
// returns 429 without a Retry-After header.
const defaultRefreshCooldown = 5 * time.Minute

type oauthCreds struct {
	AccessToken      string
	RefreshToken     string
	ExpiresAt        time.Time
	SubscriptionType string
}

// Account owns one credentials file. It must be the *only* consumer of the
// underlying OAuth grant: refresh tokens are single-use, so any other process
// refreshing the same grant invalidates ours (and vice versa).
type Account struct {
	Name string

	path      string
	client    *Client
	noRefresh bool
	gate      *refresh.Gate // global refresh queue; nil runs refreshes directly

	mu    sync.Mutex
	top   map[string]json.RawMessage // full file; unknown fields preserved on save
	oauth map[string]json.RawMessage // claudeAiOauth object, ditto
	creds oauthCreds
	dirty bool // creds rotated in memory but not yet persisted

	// refreshNotBefore throttles the token endpoint after a 429: until it
	// elapses, neither proactive (Token) nor reactive (Refresh) refresh will
	// call out. Zero means no cooldown.
	refreshNotBefore time.Time
}

func LoadAccount(name, path string, client *Client, noRefresh bool, gate *refresh.Gate) (*Account, error) {
	a := &Account{Name: name, path: path, client: client, noRefresh: noRefresh, gate: gate}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if err := a.parse(data); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return a, nil
}

func (a *Account) parse(data []byte) error {
	if err := json.Unmarshal(data, &a.top); err != nil {
		return fmt.Errorf("parse credentials: %w", err)
	}
	raw, ok := a.top["claudeAiOauth"]
	if !ok {
		return errors.New("missing claudeAiOauth key")
	}
	if err := json.Unmarshal(raw, &a.oauth); err != nil {
		return fmt.Errorf("parse claudeAiOauth: %w", err)
	}
	a.creds.AccessToken = rawString(a.oauth["accessToken"])
	a.creds.RefreshToken = rawString(a.oauth["refreshToken"])
	a.creds.SubscriptionType = rawString(a.oauth["subscriptionType"])
	a.creds.ExpiresAt = parseExpiresAt(a.oauth["expiresAt"])
	if a.creds.AccessToken == "" {
		return errors.New("missing claudeAiOauth.accessToken")
	}
	return nil
}

func rawString(raw json.RawMessage) string {
	var s string
	if raw == nil || json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return s
}

// parseExpiresAt accepts epoch milliseconds (what Claude Code writes) or an
// RFC 3339 string. Zero time means unknown and is treated as expired.
func parseExpiresAt(raw json.RawMessage) time.Time {
	if raw == nil {
		return time.Time{}
	}
	var ms int64
	if err := json.Unmarshal(raw, &ms); err == nil {
		return time.UnixMilli(ms)
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

func (a *Account) SubscriptionType() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.creds.SubscriptionType
}

// Token returns a valid access token, refreshing first when it is within
// refreshSkew of expiry. With noRefresh it always returns the stored token.
func (a *Account) Token(ctx context.Context) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.dirty {
		_ = a.saveLocked() // retry persisting a previously failed write
	}
	if a.noRefresh || time.Until(a.creds.ExpiresAt) > refreshSkew {
		return a.creds.AccessToken, nil
	}
	// Within the refresh window. A prior 429 on the token endpoint puts us in a
	// cooldown: don't touch it again until that elapses, so neither periodic
	// polls nor manual refreshes can hammer an already-rate-limited endpoint.
	if !a.canRefreshLocked() {
		if time.Now().Before(a.creds.ExpiresAt) {
			return a.creds.AccessToken, nil // token still valid; ride it out
		}
		return "", a.cooldownErrLocked() // expired and cooling down: fail fast
	}
	if err := a.refreshLocked(ctx); err != nil {
		// Refresh failed, but the current token may still be valid for the rest
		// of the skew window — prefer serving it over failing the whole poll.
		if time.Now().Before(a.creds.ExpiresAt) {
			return a.creds.AccessToken, nil
		}
		return "", err
	}
	return a.creds.AccessToken, nil
}

// canRefreshLocked reports whether the token-endpoint cooldown (set after a 429)
// has elapsed. Caller must hold a.mu.
func (a *Account) canRefreshLocked() bool {
	return a.refreshNotBefore.IsZero() || !time.Now().Before(a.refreshNotBefore)
}

// cooldownErrLocked reports the remaining cooldown as a RateLimitedError so the
// poller backs off (and honors the remaining time) without hitting the network.
func (a *Account) cooldownErrLocked() error {
	return &RateLimitedError{RetryAfter: time.Until(a.refreshNotBefore)}
}

// setRefreshCooldownLocked records how long to avoid the token endpoint after a
// 429: the server's Retry-After when given, else a default. Caller holds a.mu.
func (a *Account) setRefreshCooldownLocked(retryAfter time.Duration) {
	if retryAfter <= 0 {
		retryAfter = defaultRefreshCooldown
	}
	a.refreshNotBefore = time.Now().Add(retryAfter)
}

// Refresh exchanges the refresh token after `rejected` got a 401. It is a
// no-op if the credentials already rotated past the rejected token.
func (a *Account) Refresh(ctx context.Context, rejected string) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.creds.AccessToken != rejected {
		return a.creds.AccessToken, nil
	}
	if a.noRefresh {
		return "", errors.New("access token rejected (401) and refresh is disabled (--no-refresh)")
	}
	if !a.canRefreshLocked() {
		return "", a.cooldownErrLocked()
	}
	if err := a.refreshLocked(ctx); err != nil {
		return "", err
	}
	return a.creds.AccessToken, nil
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
}

// refreshLocked exchanges the refresh token, funneling the actual exchange
// through the global refresh gate so concurrent expiries don't stampede the
// token endpoint. Caller must hold a.mu.
func (a *Account) refreshLocked(ctx context.Context) error {
	if a.creds.RefreshToken == "" {
		return errors.New("no refresh token in credentials file")
	}
	if a.gate == nil {
		return a.doRefreshLocked(ctx)
	}
	return a.gate.Do(ctx, func() error { return a.doRefreshLocked(ctx) })
}

func (a *Account) doRefreshLocked(ctx context.Context) error {
	body, err := json.Marshal(map[string]string{
		"grant_type":    "refresh_token",
		"refresh_token": a.creds.RefreshToken,
		"client_id":     ClientID,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.client.TokenURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", UserAgent)

	resp, err := a.client.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("refresh token: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		rl := &RateLimitedError{}
		if secs, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && secs > 0 {
			rl.RetryAfter = time.Duration(secs) * time.Second
		}
		a.setRefreshCooldownLocked(rl.RetryAfter)
		return rl
	}
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("refresh token: %s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	var tr tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return fmt.Errorf("refresh token: decode response: %w", err)
	}
	if tr.AccessToken == "" {
		return errors.New("refresh token: response missing access_token")
	}

	a.creds.AccessToken = tr.AccessToken
	if tr.RefreshToken != "" {
		a.creds.RefreshToken = tr.RefreshToken
	}
	a.creds.ExpiresAt = time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)
	a.dirty = true
	a.refreshNotBefore = time.Time{} // success clears any cooldown
	// The grant already rotated server-side; even if persisting fails the new
	// tokens must stay in memory or the account is bricked. Token() retries
	// the write on later polls.
	if err := a.saveLocked(); err != nil {
		return fmt.Errorf("credentials refreshed but not yet persisted to %s: %w", a.path, err)
	}
	return nil
}

// saveLocked atomically rewrites the credentials file (temp file + rename,
// 0600), preserving any fields this program does not understand.
func (a *Account) saveLocked() error {
	a.oauth["accessToken"], _ = json.Marshal(a.creds.AccessToken)
	a.oauth["refreshToken"], _ = json.Marshal(a.creds.RefreshToken)
	a.oauth["expiresAt"], _ = json.Marshal(a.creds.ExpiresAt.UnixMilli())
	oauthRaw, err := json.Marshal(a.oauth)
	if err != nil {
		return err
	}
	a.top["claudeAiOauth"] = oauthRaw
	data, err := json.MarshalIndent(a.top, "", "  ")
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp(filepath.Dir(a.path), CredentialsFile+".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op once renamed
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), a.path); err != nil {
		return err
	}
	a.dirty = false
	return nil
}
