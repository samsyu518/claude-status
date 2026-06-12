// Package poller periodically fetches usage for each account and records the
// result in the store.
package poller

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"go-gin-claude-status/internal/anthropic"
	"go-gin-claude-status/internal/store"
)

// MinInterval keeps the upstream polling rate well below anything that could
// look like abuse.
const MinInterval = 180 * time.Second

const maxBackoff = 30 * time.Minute

type Poller struct {
	Account  *anthropic.Account
	Client   *anthropic.Client
	Store    *store.Store
	Interval time.Duration
}

// Run polls until ctx is cancelled. Failures back off exponentially (capped
// at maxBackoff, or the server's Retry-After if longer) while the store keeps
// serving the last good snapshot.
func (p *Poller) Run(ctx context.Context) {
	interval := max(p.Interval, MinInterval)
	backoff := interval
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		if err := p.poll(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			backoff = min(backoff*2, maxBackoff)
			wait := backoff
			var rl *anthropic.RateLimitedError
			if errors.As(err, &rl) && rl.RetryAfter > wait {
				wait = rl.RetryAfter
			}
			log.Printf("[%s] poll failed: %v (next attempt in %s)", p.Account.Name, err, wait)
			timer.Reset(wait)
			continue
		}
		backoff = interval
		timer.Reset(interval)
	}
}

func (p *Poller) poll(ctx context.Context) error {
	token, err := p.Account.Token(ctx)
	if err == nil {
		var usage *anthropic.Usage
		usage, err = p.Client.FetchUsage(ctx, token)
		if errors.Is(err, anthropic.ErrUnauthorized) {
			if token, err = p.Account.Refresh(ctx, token); err == nil {
				usage, err = p.Client.FetchUsage(ctx, token)
			}
		}
		if err == nil {
			p.Store.SetUsage(p.Account.Name, p.Account.SubscriptionType(), usage)
			log.Printf("[%s] usage fetched: 5h %s, 7d %s", p.Account.Name, pct(usage.FiveHour), pct(usage.SevenDay))
			return nil
		}
	}
	p.Store.SetError(p.Account.Name, err)
	return err
}

func pct(w *anthropic.Window) string {
	if w == nil {
		return "n/a"
	}
	return fmt.Sprintf("%.0f%%", w.Utilization)
}
