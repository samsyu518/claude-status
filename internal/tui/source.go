package tui

import (
	"context"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"go-gin-claude-status/internal/lock"
	"go-gin-claude-status/internal/view"
)

// HostFunc brings up the in-process backend on the given listener. It runs
// until ctx is cancelled. It is invoked at most once per Source — when this
// process wins the backend role.
type HostFunc func(ctx context.Context, ln net.Listener) error

// Config drives a Source.
//
//   - Auto mode: BindAddr + LockPath + Host all set. The Source either wins
//     the lock and hosts, or attaches as a client to the running backend, and
//     promotes itself on failover.
//   - Remote mode: only RemoteURL set (BindAddr == ""). Pure client, never
//     hosts, never touches the lock.
type Config struct {
	BindAddr  string
	LockPath  string
	Host      HostFunc
	RemoteURL string // http://host:port  (remote/pure-client mode)
}

// Backend is what the TUI reads from. It hides whether this process is the
// host or a client, and survives a host handover underneath.
type Backend interface {
	View() view.Data
	Mode() string // "host" | "client"
	Refresh(ctx context.Context) error
}

// Source implements Backend with port/lock coordination and failover.
type Source struct {
	cfg    Config
	client *http.Client

	mu      sync.RWMutex
	mode    string
	cached  view.Data
	wsURL   string
	baseURL string
	lk      *lock.Lock // non-nil ⇒ we are the host
}

// Connect establishes the backend role, seeds the cached view with the first
// WS frame, and starts the background maintenance loop.
func Connect(ctx context.Context, cfg Config) (*Source, error) {
	s := &Source{cfg: cfg, client: &http.Client{Timeout: 10 * time.Second}}

	if cfg.BindAddr == "" { // explicit remote: pure client
		base := strings.TrimRight(cfg.RemoteURL, "/")
		s.wsURL = "ws" + strings.TrimPrefix(base, "http") + "/ws"
		s.baseURL = base
		s.mode = "client"
	} else {
		if err := s.decide(ctx); err != nil {
			return nil, err
		}
	}

	// Dial WS and read the first frame to seed cached before returning.
	conn, err := s.dialWithRetry(ctx)
	if err != nil {
		return nil, err
	}
	var data view.Data
	if err := conn.ReadJSON(&data); err != nil {
		conn.Close()
		return nil, err
	}
	s.mu.Lock()
	s.cached = data
	s.mu.Unlock()

	go s.loop(ctx, conn)
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
		return err
	}
	s.mu.Lock()
	s.mode = "client"
	s.wsURL = "ws://" + info.Addr + "/ws"
	s.baseURL = "http://" + info.Addr
	s.mu.Unlock()
	return nil
}

// becomeHost binds the port and brings up the backend. The caller must already
// hold lk; on success the Source takes ownership of it.
func (s *Source) becomeHost(ctx context.Context, lk *lock.Lock) error {
	ln, err := net.Listen("tcp", s.cfg.BindAddr)
	if err != nil {
		return err
	}
	if err := s.cfg.Host(ctx, ln); err != nil {
		ln.Close()
		return err
	}
	if err := lk.WriteInfo(lock.Info{Addr: s.cfg.BindAddr, PID: os.Getpid()}); err != nil {
		return err
	}
	s.mu.Lock()
	s.mode = "host"
	s.wsURL = "ws://" + s.cfg.BindAddr + "/ws"
	s.baseURL = "http://" + s.cfg.BindAddr
	s.lk = lk
	s.mu.Unlock()
	return nil
}

func (s *Source) View() view.Data {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cached
}

func (s *Source) Mode() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.mode
}

func (s *Source) Refresh(ctx context.Context) error {
	s.mu.RLock()
	baseURL := s.baseURL
	s.mu.RUnlock()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/refresh", nil)
	if err != nil {
		return err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// loop maintains the WS connection, updating cached on each incoming frame.
// On disconnect it attempts failover (client) or redialing (host), then
// reconnects.
func (s *Source) loop(ctx context.Context, conn *websocket.Conn) {
	defer func() {
		s.mu.Lock()
		if s.lk != nil {
			s.lk.Release()
			s.lk = nil
		}
		s.mu.Unlock()
	}()

	for {
		// Read frames from the current connection until it closes.
		for {
			var data view.Data
			if err := conn.ReadJSON(&data); err != nil {
				conn.Close()
				break
			}
			s.mu.Lock()
			s.cached = data
			s.mu.Unlock()
		}

		if ctx.Err() != nil {
			return
		}

		// Connection dropped. If we're a client, try to take over or re-point.
		s.mu.RLock()
		isHost := s.lk != nil
		s.mu.RUnlock()

		if !isHost {
			s.tryTakeover(ctx)
		}

		if ctx.Err() != nil {
			return
		}

		// Redial with retries.
		var err error
		if conn, err = s.dialWithRetry(ctx); err != nil {
			return
		}
		// Seed cache with the first frame from the new connection.
		var data view.Data
		if err := conn.ReadJSON(&data); err != nil {
			conn.Close()
			continue
		}
		s.mu.Lock()
		s.cached = data
		s.mu.Unlock()
	}
}

// dialWithRetry dials wsURL, retrying briefly to handle the race where a
// freshly-started server hasn't bound yet.
func (s *Source) dialWithRetry(ctx context.Context) (*websocket.Conn, error) {
	s.mu.RLock()
	wsURL := s.wsURL
	s.mu.RUnlock()
	var (
		conn *websocket.Conn
		err  error
	)
	for range 20 {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		conn, _, err = websocket.DefaultDialer.DialContext(ctx, wsURL, nil)
		if err == nil {
			return conn, nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return nil, err
}

// tryTakeover races for the freed lock. The winner becomes host; the loser
// re-points to whoever won.
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
		s.wsURL = "ws://" + info.Addr + "/ws"
		s.baseURL = "http://" + info.Addr
		s.mu.Unlock()
	}
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
