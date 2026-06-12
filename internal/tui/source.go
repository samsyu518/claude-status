package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"go-gin-claude-status/internal/lock"
	"go-gin-claude-status/internal/store"
)

// HostFunc brings up the in-process backend on the given listener and returns a
// provider reading its store. It runs until ctx is cancelled. It is invoked at
// most once per Source — when this process wins the backend role.
type HostFunc func(ctx context.Context, ln net.Listener) (func() []store.Snapshot, error)

// Config drives a Source.
//
//   - Auto mode: BindAddr + URL + LockPath + Host all set. The Source either
//     wins the lock and hosts, or attaches as a client to the running backend,
//     and promotes itself on failover.
//   - Remote mode: only URL set (BindAddr == ""). Pure client, never hosts,
//     never touches the lock.
type Config struct {
	BindAddr string
	URL      string
	LockPath string
	Host     HostFunc
	Poll     time.Duration // client poll / failover-check interval (default 5s)
}

// Backend is what the TUI reads from. It hides whether this process is the host
// or a client, and survives a host handover underneath.
type Backend interface {
	Snapshots() []store.Snapshot
	Mode() string // "host" | "client"
}

// Source implements Backend with port/lock coordination and failover.
type Source struct {
	cfg    Config
	client *http.Client

	mu     sync.RWMutex
	mode   string
	inproc func() []store.Snapshot // non-nil ⇒ we are the host
	cached []store.Snapshot        // client: last good fetch
	url    string                  // client: current backend endpoint
	lk     *lock.Lock              // host: held for our lifetime
}

// Connect establishes the backend role and starts the background loop.
func Connect(ctx context.Context, cfg Config) (*Source, error) {
	if cfg.Poll <= 0 {
		cfg.Poll = 5 * time.Second
	}
	s := &Source{cfg: cfg, client: &http.Client{Timeout: 10 * time.Second}}

	if cfg.BindAddr == "" { // explicit remote: pure client
		s.url = cfg.URL
		s.mode = "client"
		snaps, err := s.fetch(ctx)
		if err != nil {
			return nil, fmt.Errorf("remote %s: %w", s.url, err)
		}
		s.cached = snaps
	} else if err := s.decide(ctx); err != nil {
		return nil, err
	}

	go s.loop(ctx)
	return s, nil
}

// decide takes the lock and either hosts or attaches as a client.
func (s *Source) decide(ctx context.Context) error {
	lk, ok, err := lock.Acquire(s.cfg.LockPath)
	if err != nil {
		return err
	}
	if ok {
		if err := s.becomeHost(ctx, lk); err != nil {
			lk.Release()
			return err
		}
		return nil
	}
	// A live backend holds the lock — read where it serves and attach.
	info, err := s.readInfoRetry()
	if err != nil {
		return fmt.Errorf("a backend holds the lock but its address is unreadable: %w", err)
	}
	s.startClient(ctx, info.Addr)
	return nil
}

// becomeHost binds the port and brings up the backend. The caller must already
// hold lk; on success the Source takes ownership of it.
func (s *Source) becomeHost(ctx context.Context, lk *lock.Lock) error {
	ln, err := net.Listen("tcp", s.cfg.BindAddr)
	if err != nil {
		return fmt.Errorf("listen %s (port busy or held by a non–claude-status process?): %w", s.cfg.BindAddr, err)
	}
	prov, err := s.cfg.Host(ctx, ln)
	if err != nil {
		ln.Close()
		return err
	}
	if err := lk.WriteInfo(lock.Info{Addr: s.cfg.BindAddr, PID: os.Getpid()}); err != nil {
		ln.Close()
		return err
	}
	s.mu.Lock()
	s.mode = "host"
	s.inproc = prov
	s.lk = lk
	s.mu.Unlock()
	return nil
}

func (s *Source) startClient(ctx context.Context, addr string) {
	s.mu.Lock()
	s.mode = "client"
	s.url = "http://" + addr + "/api/usage"
	s.mu.Unlock()
	if snaps, err := s.fetch(ctx); err == nil {
		s.mu.Lock()
		s.cached = snaps
		s.mu.Unlock()
	}
}

func (s *Source) Snapshots() []store.Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.inproc != nil {
		return s.inproc()
	}
	return s.cached
}

func (s *Source) Mode() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.mode
}

// loop polls the remote in client mode and, on failure, attempts to take over
// the backend role. The host does nothing here (the TUI reads its store
// directly each tick).
func (s *Source) loop(ctx context.Context) {
	t := time.NewTicker(s.cfg.Poll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			s.mu.Lock()
			if s.lk != nil {
				s.lk.Release()
				s.lk = nil
			}
			s.mu.Unlock()
			return
		case <-t.C:
			s.mu.RLock()
			host := s.inproc != nil
			s.mu.RUnlock()
			if host {
				continue
			}
			if snaps, err := s.fetch(ctx); err == nil {
				s.mu.Lock()
				s.cached = snaps
				s.mu.Unlock()
				continue
			}
			if s.cfg.Host != nil && s.cfg.BindAddr != "" {
				s.tryTakeover(ctx)
			}
		}
	}
}

// tryTakeover races for the freed lock. The winner hosts; the rest re-point to
// whoever won.
func (s *Source) tryTakeover(ctx context.Context) {
	lk, ok, err := lock.Acquire(s.cfg.LockPath)
	if err != nil {
		return
	}
	if ok {
		if err := s.becomeHost(ctx, lk); err != nil {
			lk.Release()
		}
		return
	}
	if info, err := s.readInfoRetry(); err == nil && info.Addr != "" {
		s.mu.Lock()
		s.url = "http://" + info.Addr + "/api/usage"
		s.mu.Unlock()
	}
}

func (s *Source) fetch(ctx context.Context) ([]store.Snapshot, error) {
	s.mu.RLock()
	url := s.url
	s.mu.RUnlock()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %s", resp.Status)
	}
	var snaps []store.Snapshot
	if err := json.NewDecoder(resp.Body).Decode(&snaps); err != nil {
		return nil, err
	}
	return snaps, nil
}

func (s *Source) readInfoRetry() (lock.Info, error) {
	var info lock.Info
	var err error
	for range 50 {
		if info, err = lock.ReadInfo(s.cfg.LockPath); err == nil && info.Addr != "" {
			return info, nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return info, err
}
