// Package refresh serializes OAuth token refreshes across all accounts and
// spaces them out in time. A burst of simultaneous refreshes (e.g. several
// accounts whose access tokens expire at the same moment) is what trips the
// token endpoint's per-IP 429 limit; funneling every refresh through one queue
// with a minimum gap turns that stampede into an orderly trickle.
package refresh

import (
	"context"
	"sync"
	"time"
)

// Gate is the global refresh queue. Every refresh runs one at a time, and
// consecutive refreshes start at least minInterval apart. Construct with New;
// the zero value is unusable.
type Gate struct {
	minInterval time.Duration

	mu   sync.Mutex // held across the whole queue → wait → run sequence
	last time.Time  // when the previous refresh began running
}

// New returns a Gate that spaces consecutive refreshes by at least
// minInterval. A non-positive interval still serializes refreshes but adds no
// delay between them.
func New(minInterval time.Duration) *Gate {
	if minInterval < 0 {
		minInterval = 0
	}
	return &Gate{minInterval: minInterval}
}

// Do runs fn as the only refresh in flight, after waiting until at least
// minInterval has elapsed since the previous refresh began. It blocks while
// queued behind other callers. If ctx is cancelled before fn starts it returns
// ctx.Err() without running fn; once fn starts it always runs to completion.
//
// The spacing clock advances even when fn fails, so a 429 does not let the next
// caller skip ahead — failed attempts still count as hits on the endpoint.
func (g *Gate) Do(ctx context.Context, fn func() error) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	if wait := g.waitLocked(); wait > 0 {
		t := time.NewTimer(wait)
		defer t.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
	g.last = time.Now()
	return fn()
}

// waitLocked reports how long until the next refresh may begin. Caller holds mu.
func (g *Gate) waitLocked() time.Duration {
	if g.last.IsZero() {
		return 0
	}
	return time.Until(g.last.Add(g.minInterval))
}
