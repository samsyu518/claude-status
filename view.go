package main

import (
	"fmt"
	"time"

	"go-gin-claude-status/internal/store"
)

// View models: everything the templates show is computed here so the
// templates stay logic-free (and the future TUI can reuse the helpers).

type viewData struct {
	Cards []cardVM
}

type cardVM struct {
	Name      string
	SubType   string
	Pending   bool // no successful fetch yet
	Error     string
	FetchedAt string
	Rows      []rowVM
	Extra     string
}

type rowVM struct {
	Label    string
	Value    int    // 0–100 for <progress>
	Class    string // daisyUI progress color modifier
	Pct      string
	ResetsIn string
	ResetsAt string
}

func buildView(snaps []store.Snapshot) viewData {
	var v viewData
	for _, s := range snaps {
		card := cardVM{
			Name:    s.Name,
			SubType: s.SubscriptionType,
			Pending: s.FetchedAt.IsZero(),
			Error:   s.Error,
			Extra:   extraLine(s.ExtraUsage),
		}
		if !s.FetchedAt.IsZero() {
			card.FetchedAt = s.FetchedAt.Local().Format("15:04:05")
		}
		card.Rows = appendRow(card.Rows, "5h", s.FiveHour)
		card.Rows = appendRow(card.Rows, "7d", s.SevenDay)
		card.Rows = appendRow(card.Rows, "7d Opus", s.SevenDayOpus)
		card.Rows = appendRow(card.Rows, "7d Sonnet", s.SevenDaySonnet)
		v.Cards = append(v.Cards, card)
	}
	return v
}

func appendRow(rows []rowVM, label string, w *store.Window) []rowVM {
	if w == nil {
		return rows
	}
	return append(rows, rowVM{
		Label:    label,
		Value:    int(w.Utilization + 0.5),
		Class:    levelClass(w.Utilization),
		Pct:      fmt.Sprintf("%.0f%%", w.Utilization),
		ResetsIn: resetsIn(w.ResetsAt),
		ResetsAt: w.ResetsAt.Local().Format("Mon 15:04"),
	})
}

func levelClass(pct float64) string {
	switch {
	case pct > 80:
		return "progress-error"
	case pct >= 50:
		return "progress-warning"
	default:
		return "progress-success"
	}
}

func resetsIn(t time.Time) string {
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

func extraLine(x *store.ExtraUsage) string {
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
