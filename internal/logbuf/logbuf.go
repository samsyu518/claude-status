// Package logbuf provides a fixed-capacity in-memory line buffer for
// displaying recent server log lines inside the TUI.
package logbuf

import (
	"strings"
	"sync"
)

// Ring is a thread-safe ring buffer of log lines that implements io.Writer.
// slog's TextHandler writes one line per record (terminated with \n), so one
// Write call produces exactly one buffered line under normal use.
type Ring struct {
	mu    sync.Mutex
	lines []string
	max   int
}

// New returns a Ring that keeps at most max lines.
func New(max int) *Ring {
	return &Ring{max: max}
}

// Write splits p on newlines and appends each non-empty line, evicting the
// oldest when the buffer is full. It always returns len(p), nil.
func (r *Ring) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for line := range strings.SplitSeq(string(p), "\n") {
		if line == "" {
			continue
		}
		if len(r.lines) >= r.max {
			r.lines = r.lines[1:]
		}
		r.lines = append(r.lines, line)
	}
	return len(p), nil
}

// Lines returns a copy of the buffered lines, oldest first.
func (r *Ring) Lines() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.lines))
	copy(out, r.lines)
	return out
}
