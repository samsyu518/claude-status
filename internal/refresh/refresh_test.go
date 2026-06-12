package refresh

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestGateSpacesConsecutiveRefreshes(t *testing.T) {
	g := New(40 * time.Millisecond)
	ctx := context.Background()

	var starts []time.Time
	for i := 0; i < 3; i++ {
		if err := g.Do(ctx, func() error {
			starts = append(starts, time.Now())
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if len(starts) != 3 {
		t.Fatalf("ran %d refreshes, want 3", len(starts))
	}
	// First runs immediately; each subsequent one waits ~minInterval.
	for i := 1; i < len(starts); i++ {
		if gap := starts[i].Sub(starts[i-1]); gap < 35*time.Millisecond {
			t.Errorf("gap[%d] = %v, want >= ~40ms", i, gap)
		}
	}
}

func TestGateSerializes(t *testing.T) {
	g := New(0) // no spacing, still one-at-a-time
	ctx := context.Background()

	var inFlight, overlapped int32
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = g.Do(ctx, func() error {
				if atomic.AddInt32(&inFlight, 1) != 1 {
					atomic.StoreInt32(&overlapped, 1)
				}
				time.Sleep(2 * time.Millisecond)
				atomic.AddInt32(&inFlight, -1)
				return nil
			})
		}()
	}
	wg.Wait()
	if overlapped != 0 {
		t.Error("two refreshes ran concurrently; gate must serialize")
	}
}

func TestGateCancelDuringWaitSkipsFn(t *testing.T) {
	g := New(time.Hour) // force a long wait on the second call
	if err := g.Do(context.Background(), func() error { return nil }); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ran := false
	err := g.Do(ctx, func() error { ran = true; return nil })
	if err == nil {
		t.Error("Do should return ctx error when cancelled during the wait")
	}
	if ran {
		t.Error("fn must not run when ctx is cancelled before it starts")
	}
}
