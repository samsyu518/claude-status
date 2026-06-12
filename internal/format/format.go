// Package format holds the display logic shared between the web UI and the
// TUI: utilization level thresholds, reset countdown text, and the extra-usage
// line. Keeping it here means both frontends grade and format the same way.
package format

import (
	"fmt"
	"time"

	"go-gin-claude-status/internal/store"
)

// Level grades a utilization percentage. Each frontend maps it to its own
// representation (web → daisyUI class, TUI → lipgloss colour).
type Level int

const (
	LevelOK   Level = iota // < 50%
	LevelWarn              // 50–80%
	LevelHigh              // > 80%
)

// LevelOf maps a utilization percentage to a Level. This is the single source
// of truth for the colour thresholds.
func LevelOf(pct float64) Level {
	switch {
	case pct > 80:
		return LevelHigh
	case pct >= 50:
		return LevelWarn
	default:
		return LevelOK
	}
}

// ResetsIn renders the countdown until a rate-limit window resets, rounded to
// the minute.
func ResetsIn(t time.Time) string {
	d := time.Until(t)
	if d <= 0 {
		return "resetting…"
	}
	d = d.Round(time.Minute)
	switch {
	case d >= 24*time.Hour:
		return fmt.Sprintf("%dd %dh", int(d.Hours())/24, int(d.Hours())%24)
	case d >= time.Hour:
		return fmt.Sprintf("%dh %02dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
}

// ExtraLine renders the optional extra-usage line, or "" when it's disabled.
func ExtraLine(x *store.ExtraUsage) string {
	if x == nil || !x.IsEnabled {
		return ""
	}
	line := "extra usage enabled"
	if x.UsedCredits != nil && x.MonthlyLimit != nil {
		line = fmt.Sprintf("extra usage: %.2f / %.2f credits", *x.UsedCredits, *x.MonthlyLimit)
	}
	if x.Utilization != nil {
		line += fmt.Sprintf(" (%.0f%%)", *x.Utilization)
	}
	return line
}
