//go:build windows

package lock

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// lockByteOffset is a fixed offset far past any real lock-file content. We lock
// a single byte there rather than byte 0 because Windows file locks are
// mandatory (unlike Unix flock, which is advisory): locking the data region
// would make the plain os.ReadFile in ReadInfo fail with a sharing violation
// while the backend holds the lock. Locking an out-of-data byte still gives
// mutual exclusion — every process contends on the same byte — while leaving
// the JSON freely readable.
const lockByteOffset = 1 << 62

// tryLock takes a non-blocking exclusive lock on f via LockFileEx. ok is false
// with a nil error when another process already holds the lock; a real error is
// returned only for unexpected failures.
func tryLock(f *os.File) (ok bool, err error) {
	ol := &windows.Overlapped{
		Offset:     uint32(lockByteOffset & 0xFFFFFFFF),
		OffsetHigh: uint32(lockByteOffset >> 32),
	}
	err = windows.LockFileEx(
		windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, // reserved
		1, // number of bytes to lock, low dword
		0, // number of bytes to lock, high dword
		ol,
	)
	if err != nil {
		// Another process already holds the lock.
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
