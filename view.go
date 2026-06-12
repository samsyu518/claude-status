package main

import (
	"fmt"

	"go-gin-claude-status/internal/format"
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
			Extra:   format.ExtraLine(s.ExtraUsage),
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
		ResetsIn: format.ResetsIn(w.ResetsAt),
		ResetsAt: w.ResetsAt.Local().Format("Mon 15:04"),
	})
}

// levelClass maps a utilization percentage to its daisyUI progress colour,
// reusing format.LevelOf so the thresholds live in one place.
func levelClass(pct float64) string {
	switch format.LevelOf(pct) {
	case format.LevelHigh:
		return "progress-error"
	case format.LevelWarn:
		return "progress-warning"
	default:
		return "progress-success"
	}
}
