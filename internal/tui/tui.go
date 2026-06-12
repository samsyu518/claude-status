// Package tui renders the per-account usage dashboard in the terminal using
// bubbletea. It is fed by a SnapshotProvider so the same model drives both the
// standalone mode (reads the in-process store) and the remote mode (polls a
// running serve daemon over HTTP).
package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"go-gin-claude-status/internal/format"
	"go-gin-claude-status/internal/store"
)

// SnapshotProvider returns the latest snapshots. Calls must be cheap and
// non-blocking — it is invoked on every tick.
type SnapshotProvider func() []store.Snapshot

const barWidth = 20

var (
	titleStyle   = lipgloss.NewStyle().Bold(true)
	subTypeStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("63"))
	mutedStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	errorStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	emptyStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))

	levelStyles = map[format.Level]lipgloss.Style{
		format.LevelOK:   lipgloss.NewStyle().Foreground(lipgloss.Color("2")),
		format.LevelWarn: lipgloss.NewStyle().Foreground(lipgloss.Color("3")),
		format.LevelHigh: lipgloss.NewStyle().Foreground(lipgloss.Color("1")),
	}
)

// Run starts the bubbletea program and blocks until the user quits or ctx is
// cancelled.
func Run(ctx context.Context, provider SnapshotProvider) error {
	m := model{provider: provider, snaps: provider()}
	_, err := tea.NewProgram(m, tea.WithAltScreen(), tea.WithContext(ctx)).Run()
	return err
}

type model struct {
	provider SnapshotProvider
	snaps    []store.Snapshot
	width    int
}

type tickMsg time.Time

func tick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m model) Init() tea.Cmd { return tick() }

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tickMsg:
		m.snaps = m.provider()
		return m, tick()
	case tea.WindowSizeMsg:
		m.width = msg.Width
		return m, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "esc", "ctrl+c":
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m model) View() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("claude-status") + mutedStyle.Render("  ·  q to quit") + "\n\n")

	if len(m.snaps) == 0 {
		b.WriteString(mutedStyle.Render("no accounts yet…") + "\n")
		return b.String()
	}

	for _, s := range m.snaps {
		b.WriteString(renderAccount(s))
		b.WriteString("\n")
	}
	return b.String()
}

func renderAccount(s store.Snapshot) string {
	var b strings.Builder

	header := titleStyle.Render(s.Name)
	if s.SubscriptionType != "" {
		header += " " + subTypeStyle.Render("["+s.SubscriptionType+"]")
	}
	if s.Error != "" {
		header += " " + errorStyle.Render("(error)")
	}
	b.WriteString(header + "\n")

	if s.FetchedAt.IsZero() {
		if s.Error != "" {
			b.WriteString("  " + errorStyle.Render("fetch failed: "+s.Error) + "\n")
		} else {
			b.WriteString("  " + mutedStyle.Render("waiting for first fetch…") + "\n")
		}
		return b.String()
	}

	b.WriteString(renderRow("5h", s.FiveHour))
	b.WriteString(renderRow("7d", s.SevenDay))
	b.WriteString(renderRow("7d Opus", s.SevenDayOpus))
	b.WriteString(renderRow("7d Sonnet", s.SevenDaySonnet))

	if extra := format.ExtraLine(s.ExtraUsage); extra != "" {
		b.WriteString("  " + mutedStyle.Render(extra) + "\n")
	}

	updated := "updated " + s.FetchedAt.Local().Format("15:04:05")
	if s.Error != "" {
		updated += " — showing last good data"
	}
	b.WriteString("  " + mutedStyle.Render(updated) + "\n")
	return b.String()
}

func renderRow(label string, w *store.Window) string {
	if w == nil {
		return ""
	}
	return fmt.Sprintf("  %-10s %s %3.0f%%   %s\n",
		label,
		renderBar(w.Utilization),
		w.Utilization,
		mutedStyle.Render(format.ResetsIn(w.ResetsAt)),
	)
}

func renderBar(pct float64) string {
	filled := int(pct/100*float64(barWidth) + 0.5)
	filled = min(max(filled, 0), barWidth)
	style := levelStyles[format.LevelOf(pct)]
	return style.Render(strings.Repeat("█", filled)) +
		emptyStyle.Render(strings.Repeat("░", barWidth-filled))
}
