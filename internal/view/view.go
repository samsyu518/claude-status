// Package view holds the precomputed view model shared by the HTTP/WS server
// (JSON wire format) and the TUI (unmarshal + live recompute of time fields).
package view

import (
	"fmt"
	"time"

	"go-gin-claude-status/internal/format"
	"go-gin-claude-status/internal/store"
)

type Data struct {
	Cards []Card `json:"cards"`
}

type Card struct {
	Name         string `json:"name"`
	SubType      string `json:"subType,omitempty"`
	Pending      bool   `json:"pending"`
	Error        string `json:"error,omitempty"`
	FetchedAt    string `json:"fetchedAt,omitempty"`
	FetchedAtISO string `json:"fetchedAtISO,omitempty"`
	Rows         []Row  `json:"rows"`
	Extra        string `json:"extra,omitempty"`
}

type Row struct {
	Label       string `json:"label"`
	Value       int    `json:"value"`
	Class       string `json:"class"`
	Pct         string `json:"pct"`
	ResetsIn    string `json:"resetsIn"`
	ResetsAt    string `json:"resetsAt"`
	ResetsAtISO string `json:"resetsAtISO"`
}

// Build computes a Data from raw snapshots. Precomputed string fields are for
// the browser; the TUI recomputes time-relative fields locally each tick using
// the ISO timestamps.
func Build(snaps []store.Snapshot) Data {
	var v Data
	for _, s := range snaps {
		card := Card{
			Name:    s.Name,
			SubType: s.SubscriptionType,
			Pending: s.FetchedAt.IsZero(),
			Error:   s.Error,
			Extra:   format.ExtraLine(s.ExtraUsage),
		}
		if !s.FetchedAt.IsZero() {
			card.FetchedAt = s.FetchedAt.Local().Format("15:04:05")
			card.FetchedAtISO = s.FetchedAt.UTC().Format(time.RFC3339)
		}
		card.Rows = appendRow(card.Rows, "5h", s.FiveHour)
		card.Rows = appendRow(card.Rows, "7d", s.SevenDay)
		for _, mw := range s.ModelWindows {
			card.Rows = appendRow(card.Rows, "7d "+mw.Name, &store.Window{Utilization: mw.Utilization, ResetsAt: mw.ResetsAt})
		}
		v.Cards = append(v.Cards, card)
	}
	return v
}

func appendRow(rows []Row, label string, w *store.Window) []Row {
	if w == nil {
		return rows
	}
	row := Row{
		Label: label,
		Value: int(w.Utilization + 0.5),
		Class: levelClass(w.Utilization),
		Pct:   fmt.Sprintf("%.0f%%", w.Utilization),
	}
	// A model-scoped window can exist with no ResetsAt yet — the account has
	// access to it but hasn't used it this period, so upstream hasn't started
	// a reset schedule for it.
	if w.ResetsAt.IsZero() {
		row.ResetsIn = "not started"
	} else {
		row.ResetsIn = format.ResetsIn(w.ResetsAt)
		row.ResetsAt = w.ResetsAt.Local().Format("Mon 15:04:05")
		row.ResetsAtISO = w.ResetsAt.UTC().Format(time.RFC3339)
	}
	return append(rows, row)
}

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
