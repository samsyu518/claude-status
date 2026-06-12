// Package store holds the latest usage snapshot per account, safe for
// concurrent use by the pollers (writers) and HTTP handlers (readers).
package store

import (
	"sync"
	"time"

	"go-gin-claude-status/internal/anthropic"
)

type Window struct {
	Utilization float64   `json:"utilization"`
	ResetsAt    time.Time `json:"resetsAt"`
}

type ExtraUsage struct {
	IsEnabled    bool     `json:"isEnabled"`
	MonthlyLimit *float64 `json:"monthlyLimit"`
	UsedCredits  *float64 `json:"usedCredits"`
	Utilization  *float64 `json:"utilization"`
}

// Snapshot is one account's latest state. Usage fields are from the last
// successful fetch (FetchedAt); Error is the latest failure, if any — the
// stale usage data is intentionally kept alongside it.
type Snapshot struct {
	Name             string      `json:"name"`
	SubscriptionType string      `json:"subscriptionType,omitempty"`
	FiveHour         *Window     `json:"fiveHour"`
	SevenDay         *Window     `json:"sevenDay"`
	SevenDayOpus     *Window     `json:"sevenDayOpus"`
	SevenDaySonnet   *Window     `json:"sevenDaySonnet"`
	ExtraUsage       *ExtraUsage `json:"extraUsage"`
	FetchedAt        time.Time   `json:"fetchedAt,omitzero"`
	Error            string      `json:"error,omitempty"`
	ErrorAt          time.Time   `json:"errorAt,omitzero"`
}

type Store struct {
	mu    sync.RWMutex
	order []string
	snaps map[string]*Snapshot
}

func New(names []string) *Store {
	s := &Store{snaps: make(map[string]*Snapshot, len(names))}
	for _, n := range names {
		s.snap(n)
	}
	return s
}

func (s *Store) SetUsage(name, subscriptionType string, u *anthropic.Usage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	snap := s.snap(name)
	snap.SubscriptionType = subscriptionType
	snap.FiveHour = window(u.FiveHour)
	snap.SevenDay = window(u.SevenDay)
	snap.SevenDayOpus = window(u.SevenDayOpus)
	snap.SevenDaySonnet = window(u.SevenDaySonnet)
	snap.ExtraUsage = extra(u.ExtraUsage)
	snap.FetchedAt = time.Now()
	snap.Error = ""
	snap.ErrorAt = time.Time{}
}

func (s *Store) SetError(name string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	snap := s.snap(name)
	snap.Error = err.Error()
	snap.ErrorAt = time.Now()
}

// Snapshots returns copies in registration order. The pointed-to values are
// never mutated after being set, so sharing them with callers is safe.
func (s *Store) Snapshots() []Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Snapshot, 0, len(s.order))
	for _, n := range s.order {
		out = append(out, *s.snaps[n])
	}
	return out
}

func (s *Store) snap(name string) *Snapshot {
	if snap, ok := s.snaps[name]; ok {
		return snap
	}
	snap := &Snapshot{Name: name}
	s.order = append(s.order, name)
	s.snaps[name] = snap
	return snap
}

func window(w *anthropic.Window) *Window {
	if w == nil {
		return nil
	}
	return &Window{Utilization: w.Utilization, ResetsAt: w.ResetsAt}
}

func extra(x *anthropic.ExtraUsage) *ExtraUsage {
	if x == nil {
		return nil
	}
	return &ExtraUsage{
		IsEnabled:    x.IsEnabled,
		MonthlyLimit: x.MonthlyLimit,
		UsedCredits:  x.UsedCredits,
		Utilization:  x.Utilization,
	}
}
