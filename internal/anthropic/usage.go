// Package anthropic talks to the same private endpoints Claude Code itself
// uses: the OAuth usage endpoint (read-only, consumes no inference quota)
// and the OAuth token refresh endpoint.
package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultUsageURL = "https://api.anthropic.com/api/oauth/usage"
	// DefaultTokenURL is the OAuth token endpoint the current Claude CLI uses;
	// it moved from console.anthropic.com (now deprecated, blanket-429s) to here.
	DefaultTokenURL = "https://platform.claude.com/v1/oauth/token"

	// ClientID is Claude Code's public PKCE OAuth client id (no secret).
	ClientID = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"

	// UserAgent must look like Claude Code on the usage endpoint; other agents
	// get persistent 429s there.
	UserAgent = "claude-code/2.1.175"

	// refreshUserAgent is what the token endpoint expects: the real CLI refreshes
	// with axios's default UA. That endpoint 429s claude-code/* and curl/* UAs
	// (the opposite of the usage endpoint), so the refresh must use this instead.
	refreshUserAgent = "axios/1.15.2"

	betaHeader = "oauth-2025-04-20"
)

// Window is one rate-limit window as returned by /api/oauth/usage.
type Window struct {
	Utilization float64   `json:"utilization"`
	ResetsAt    time.Time `json:"resets_at"`
}

type ExtraUsage struct {
	IsEnabled    bool     `json:"is_enabled"`
	MonthlyLimit *float64 `json:"monthly_limit"`
	UsedCredits  *float64 `json:"used_credits"`
	Utilization  *float64 `json:"utilization"`
}

type Usage struct {
	FiveHour       *Window     `json:"five_hour"`
	SevenDay       *Window     `json:"seven_day"`
	SevenDayOpus   *Window     `json:"seven_day_opus"`
	SevenDaySonnet *Window     `json:"seven_day_sonnet"`
	ExtraUsage     *ExtraUsage `json:"extra_usage"`
}

// ErrUnauthorized signals the access token was rejected and a refresh should
// be attempted.
var ErrUnauthorized = errors.New("access token rejected (401)")

type RateLimitedError struct {
	RetryAfter time.Duration // zero when the server did not say
}

func (e *RateLimitedError) Error() string {
	if e.RetryAfter > 0 {
		return fmt.Sprintf("rate limited (429), retry after %s", e.RetryAfter)
	}
	return "rate limited (429)"
}

type Client struct {
	HTTP     *http.Client
	UsageURL string
	TokenURL string
}

func NewClient() *Client {
	return &Client{
		HTTP:     &http.Client{Timeout: 30 * time.Second},
		UsageURL: DefaultUsageURL,
		TokenURL: DefaultTokenURL,
	}
}

func (c *Client) FetchUsage(ctx context.Context, accessToken string) (*Usage, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.UsageURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("anthropic-beta", betaHeader)
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch usage: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		var u Usage
		if err := json.NewDecoder(resp.Body).Decode(&u); err != nil {
			return nil, fmt.Errorf("decode usage response: %w", err)
		}
		return &u, nil
	case http.StatusUnauthorized:
		return nil, ErrUnauthorized
	case http.StatusTooManyRequests:
		rl := &RateLimitedError{}
		if secs, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && secs > 0 {
			rl.RetryAfter = time.Duration(secs) * time.Second
		}
		return nil, rl
	default:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("usage endpoint returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
}
