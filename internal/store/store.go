// Package store holds the latest usage snapshot per account, safe for
// concurrent use by the pollers (writers) and HTTP handlers (readers).
package store

import (
	"sort"
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

// ModelWindow is one model-scoped weekly window (Opus, Sonnet, Fable, ...),
// derived from anthropic.Usage.Limits.
type ModelWindow struct {
	Name        string    `json:"name"`
	Utilization float64   `json:"utilization"`
	ResetsAt    time.Time `json:"resetsAt"`
}

// Snapshot is one account's latest state. Usage fields are from the last
// successful fetch (FetchedAt); Error is the latest failure, if any — the
// stale usage data is intentionally kept alongside it.
type Snapshot struct {
	Name             string        `json:"name"`
	SubscriptionType string        `json:"subscriptionType,omitempty"`
	FiveHour         *Window       `json:"fiveHour"`
	SevenDay         *Window       `json:"sevenDay"`
	ModelWindows     []ModelWindow `json:"modelWindows"`
	ExtraUsage       *ExtraUsage   `json:"extraUsage"`
	FetchedAt        time.Time     `json:"fetchedAt,omitzero"`
	Error            string        `json:"error,omitempty"`
	ErrorAt          time.Time     `json:"errorAt,omitzero"`
}

type Store struct {
	mu    sync.RWMutex
	order []string
	snaps map[string]*Snapshot

	subsMu sync.Mutex
	subs   map[int]chan []Snapshot
	nextID int
}

func New(names []string) *Store {
	s := &Store{
		snaps: make(map[string]*Snapshot, len(names)),
		subs:  make(map[int]chan []Snapshot),
	}
	for _, n := range names {
		s.snap(n)
	}
	return s
}

func (s *Store) SetUsage(name, subscriptionType string, u *anthropic.Usage) {
	s.mu.Lock()
	snap := s.snap(name)
	snap.SubscriptionType = subscriptionType
	snap.FiveHour = window(u.FiveHour)
	snap.SevenDay = window(u.SevenDay)
	snap.ModelWindows = modelWindows(u.Limits)
	snap.ExtraUsage = extra(u.ExtraUsage)
	snap.FetchedAt = time.Now()
	snap.Error = ""
	snap.ErrorAt = time.Time{}
	snaps := s.snapshotsLocked()
	s.mu.Unlock()
	s.broadcast(snaps)
}

func (s *Store) SetError(name string, err error) {
	s.mu.Lock()
	snap := s.snap(name)
	snap.Error = err.Error()
	snap.ErrorAt = time.Now()
	snaps := s.snapshotsLocked()
	s.mu.Unlock()
	s.broadcast(snaps)
}

// Snapshots returns copies in registration order. The pointed-to values are
// never mutated after being set, so sharing them with callers is safe.
func (s *Store) Snapshots() []Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snapshotsLocked()
}

// Subscribe returns a buffered channel that receives a snapshot list on every
// store change. The caller must invoke the returned cancel func to unsubscribe.
func (s *Store) Subscribe() (<-chan []Snapshot, func()) {
	ch := make(chan []Snapshot, 1)
	s.subsMu.Lock()
	id := s.nextID
	s.nextID++
	s.subs[id] = ch
	s.subsMu.Unlock()
	return ch, func() {
		s.subsMu.Lock()
		delete(s.subs, id)
		s.subsMu.Unlock()
	}
}

func (s *Store) broadcast(snaps []Snapshot) {
	s.subsMu.Lock()
	defer s.subsMu.Unlock()
	for _, ch := range s.subs {
		select {
		case ch <- snaps:
		default: // slow consumer: drop; it gets the next update
		}
	}
}

func (s *Store) snapshotsLocked() []Snapshot {
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

// modelWindows derives one row per model-scoped limit — shown as soon as the
// account has that model scope at all, even before it has any usage this
// period (ResetsAt zero then; view.appendRow renders that as "not started"
// instead of a bogus countdown).
func modelWindows(limits []anthropic.Limit) []ModelWindow {
	var out []ModelWindow
	for _, l := range limits {
		name := l.ModelName()
		if name == "" {
			continue
		}
		out = append(out, ModelWindow{Name: name, Utilization: l.Percent, ResetsAt: l.ResetsAt})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
