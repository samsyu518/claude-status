// Package lock provides a single-owner gate for the backend process. Exactly
// one process may hold the flock for a given accounts directory at a time —
// that process is the sole backend (and the sole token refresher), regardless
// of which TCP port it binds. The lock file also records the live backend's
// address so clients can find it (and so a mis-typed --listen attaches to the
// real backend instead of spawning a second one).
package lock

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// Info is the backend coordinate recorded inside the lock file.
type Info struct {
	Addr string `json:"addr"`
	PID  int    `json:"pid"`
}

// Lock holds an acquired flock. The OS releases it automatically when the file
// descriptor is closed, including on process crash — so there are no stale locks.
type Lock struct {
	f *os.File
}

// PathFor maps an accounts directory to its lock file. The identity is the
// absolute accounts dir: the same account set shares one lock; distinct sets
// get distinct locks.
func PathFor(accountsDir string) (string, error) {
	abs, err := filepath.Abs(accountsDir)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(abs))
	dir := filepath.Join(runtimeDir(), "claude-status")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return filepath.Join(dir, hex.EncodeToString(sum[:8])+".lock"), nil
}

func runtimeDir() string {
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" {
		return d
	}
	return os.TempDir()
}

// Acquire takes a non-blocking exclusive flock. ok is false (with nil error)
// when another live process already holds it.
func Acquire(path string) (l *Lock, ok bool, err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return &Lock{f: f}, true, nil
}

// WriteInfo records the backend coordinate in place. It must NOT rename a new
// file over the lock path: rename swaps the inode, which would leave the held
// fd locking the old (unlinked) inode while a fresh open locks a different one,
// breaking mutual exclusion.
func (l *Lock) WriteInfo(info Info) error {
	data, err := json.Marshal(info)
	if err != nil {
		return err
	}
	if err := l.f.Truncate(int64(len(data))); err != nil {
		return err
	}
	if _, err := l.f.WriteAt(data, 0); err != nil {
		return err
	}
	return l.f.Sync()
}

// ReadInfo reads the recorded backend coordinate. An empty/half-written file
// yields an error; callers should retry briefly during the startup race.
func ReadInfo(path string) (Info, error) {
	var info Info
	data, err := os.ReadFile(path)
	if err != nil {
		return info, err
	}
	if len(data) == 0 {
		return info, fmt.Errorf("lock file %s is empty", path)
	}
	if err := json.Unmarshal(data, &info); err != nil {
		return info, err
	}
	return info, nil
}

// Release closes the descriptor, releasing the flock.
func (l *Lock) Release() error {
	return l.f.Close()
}
