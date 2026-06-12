//go:build !windows

package lock

import (
	"errors"
	"os"
	"syscall"
)

// tryLock takes a non-blocking exclusive advisory lock (flock) on f. ok is
// false with a nil error when another process already holds the lock; a real
// error is returned only for unexpected failures.
func tryLock(f *os.File) (ok bool, err error) {
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
