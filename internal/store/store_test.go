package store

import (
	"errors"
	"sync"
	"testing"
	"time"

	"go-gin-claude-status/internal/anthropic"
)

func TestStoreConcurrent(t *testing.T) {
	st := New([]string{"a", "b"})
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range 100 {
				st.SetUsage("a", "max", &anthropic.Usage{
					FiveHour: &anthropic.Window{Utilization: float64(j)},
				})
				st.SetError("b", errors.New("boom"))
				_ = st.Snapshots()
			}
		}()
	}
	wg.Wait()

	snaps := st.Snapshots()
	if len(snaps) != 2 || snaps[0].Name != "a" || snaps[1].Name != "b" {
		t.Fatalf("snapshots = %+v", snaps)
	}
	if snaps[0].Error != "" || snaps[0].FiveHour == nil {
		t.Errorf("a = %+v", snaps[0])
	}
	if snaps[1].Error != "boom" {
		t.Errorf("b = %+v", snaps[1])
	}
}

func TestErrorKeepsLastGoodData(t *testing.T) {
	st := New([]string{"a"})
	st.SetUsage("a", "pro", &anthropic.Usage{FiveHour: &anthropic.Window{Utilization: 42}})
	st.SetError("a", errors.New("upstream down"))

	s := st.Snapshots()[0]
	if s.FiveHour == nil || s.FiveHour.Utilization != 42 {
		t.Errorf("stale data lost: %+v", s.FiveHour)
	}
	if s.Error != "upstream down" || s.FetchedAt.IsZero() || s.ErrorAt.IsZero() {
		t.Errorf("snapshot = %+v", s)
	}

	st.SetUsage("a", "pro", &anthropic.Usage{FiveHour: &anthropic.Window{Utilization: 43}})
	if s := st.Snapshots()[0]; s.Error != "" || !s.ErrorAt.IsZero() {
		t.Errorf("error not cleared on success: %+v", s)
	}
}

func TestSubscribeDelivers(t *testing.T) {
	st := New([]string{"a"})
	ch, cancel := st.Subscribe()
	defer cancel()

	st.SetUsage("a", "pro", &anthropic.Usage{FiveHour: &anthropic.Window{Utilization: 55}})

	select {
	case snaps := <-ch:
		if len(snaps) != 1 || snaps[0].FiveHour == nil || snaps[0].FiveHour.Utilization != 55 {
			t.Fatalf("wrong snapshot: %+v", snaps)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for broadcast")
	}
}

func TestUnsubscribeStopsDelivery(t *testing.T) {
	st := New([]string{"a"})
	ch, cancel := st.Subscribe()
	cancel() // unsubscribe immediately

	st.SetUsage("a", "pro", &anthropic.Usage{})

	select {
	case <-ch:
		t.Fatal("received after unsubscribe")
	case <-time.After(50 * time.Millisecond):
		// expected
	}
}
