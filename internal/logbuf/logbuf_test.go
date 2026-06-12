package logbuf

import (
	"testing"
)

func TestWrite(t *testing.T) {
	r := New(5)
	r.Write([]byte("line1\nline2\nline3\n"))
	got := r.Lines()
	if len(got) != 3 {
		t.Fatalf("want 3 lines, got %d: %v", len(got), got)
	}
	if got[0] != "line1" || got[1] != "line2" || got[2] != "line3" {
		t.Errorf("unexpected lines: %v", got)
	}
}

func TestCapacity(t *testing.T) {
	r := New(3)
	for range 5 {
		r.Write([]byte("x\n"))
	}
	got := r.Lines()
	if len(got) != 3 {
		t.Fatalf("want 3 lines after overflow, got %d: %v", len(got), got)
	}
}

func TestLinesCopy(t *testing.T) {
	r := New(10)
	r.Write([]byte("a\n"))
	lines := r.Lines()
	lines[0] = "mutated"
	if r.Lines()[0] != "a" {
		t.Error("Lines() must return a copy, not expose the internal slice")
	}
}

func TestMultiLineWrite(t *testing.T) {
	r := New(10)
	r.Write([]byte("one\ntwo\n"))
	got := r.Lines()
	if len(got) != 2 || got[0] != "one" || got[1] != "two" {
		t.Errorf("unexpected: %v", got)
	}
}

func TestEmptyLines(t *testing.T) {
	r := New(5)
	r.Write([]byte("\n\n"))
	if got := r.Lines(); len(got) != 0 {
		t.Errorf("empty lines should be skipped, got %v", got)
	}
}

func TestOldestEvicted(t *testing.T) {
	r := New(3)
	r.Write([]byte("a\nb\nc\nd\n"))
	got := r.Lines()
	if len(got) != 3 || got[0] != "b" || got[1] != "c" || got[2] != "d" {
		t.Errorf("oldest should be evicted, got %v", got)
	}
}
