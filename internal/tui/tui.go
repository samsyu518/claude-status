// Package tui renders the per-account usage dashboard in the terminal using
// bubbletea. It reads from a Backend, which transparently is either the
// in-process host server or a remote client — and may hand the host role over
// underneath without the view caring.
package tui

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"go-gin-claude-status/internal/format"
	"go-gin-claude-status/internal/view"
)

const barWidth = 20
const logTailLines = 8

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

// LogReader provides buffered log lines for display in the TUI.
// logbuf.Ring satisfies this interface automatically.
type LogReader interface {
	Lines() []string
}

// Run starts the bubbletea program and blocks until the user quits or ctx is
// cancelled. Pass a non-nil logs to show recent server log lines in host mode.
func Run(ctx context.Context, b Backend, logs LogReader) error {
	loc, tzLabel := resolveTZ()
	m := model{
		backend: b,
		data:    b.View(),
		logs:    logs,
		loc:     loc,
		tzLabel: tzLabel,
		ctx:     ctx,
	}
	_, err := tea.NewProgram(m, tea.WithAltScreen(), tea.WithContext(ctx)).Run()
	return err
}

// resolveTZ returns a timezone location and display label for reset times.
// Uses the TZ env var (IANA name) when set; falls back to the system local zone.
func resolveTZ() (*time.Location, string) {
	if tz := os.Getenv("TZ"); tz != "" {
		if loc, err := time.LoadLocation(tz); err == nil {
			return loc, tz
		}
	}
	name, _ := time.Now().Zone()
	return time.Local, name
}

type model struct {
	backend    Backend
	data       view.Data
	ctx        context.Context
	width      int
	logs       LogReader
	logLines   []string
	loc        *time.Location
	tzLabel    string
	refreshing bool
}

type tickMsg time.Time
type refreshDoneMsg struct{}

func tick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m model) Init() tea.Cmd { return tick() }

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tickMsg:
		m.data = m.backend.View()
		if m.logs != nil {
			m.logLines = m.logs.Lines()
		}
		return m, tick()
	case refreshDoneMsg:
		m.refreshing = false
		return m, nil
	case tea.WindowSizeMsg:
		m.width = msg.Width
		return m, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "esc", "ctrl+c":
			return m, tea.Quit
		case "r", "R":
			if !m.refreshing {
				m.refreshing = true
				ctx := m.ctx
				b := m.backend
				return m, func() tea.Msg {
					b.Refresh(ctx) //nolint:errcheck
					return refreshDoneMsg{}
				}
			}
		}
	}
	return m, nil
}

func (m model) View() string {
	var b strings.Builder

	help := "q to quit · r to refresh"
	if m.refreshing {
		help = "refreshing…"
	}
	b.WriteString(titleStyle.Render("claude-status") +
		subTypeStyle.Render(" ["+m.backend.Mode()+"]") +
		mutedStyle.Render("  ·  "+help) + "\n\n")

	if len(m.data.Cards) == 0 {
		b.WriteString(mutedStyle.Render("no accounts yet…") + "\n")
		return b.String()
	}

	for _, card := range m.data.Cards {
		b.WriteString(renderAccount(card, m.loc, m.tzLabel))
		b.WriteString("\n")
	}

	if m.backend.Mode() == "host" && len(m.logLines) > 0 {
		b.WriteString("\n" + mutedStyle.Render("log") + "\n")
		for _, ln := range tail(m.logLines, logTailLines) {
			b.WriteString("  " + mutedStyle.Render(truncate(ln, m.width)) + "\n")
		}
	}

	return b.String()
}

func tail(lines []string, n int) []string {
	if len(lines) <= n {
		return lines
	}
	return lines[len(lines)-n:]
}

func truncate(s string, w int) string {
	if w <= 0 || len(s) <= w {
		return s
	}
	return s[:w]
}

func renderAccount(card view.Card, loc *time.Location, tzLabel string) string {
	var b strings.Builder

	header := titleStyle.Render(card.Name)
	if card.SubType != "" {
		header += " " + subTypeStyle.Render("["+card.SubType+"]")
	}
	if card.Error != "" {
		header += " " + errorStyle.Render("(error)")
	}
	b.WriteString(header + "\n")

	if card.Pending {
		if card.Error != "" {
			b.WriteString("  " + errorStyle.Render("fetch failed: "+card.Error) + "\n")
		} else {
			b.WriteString("  " + mutedStyle.Render("waiting for first fetch…") + "\n")
		}
		return b.String()
	}

	for _, row := range card.Rows {
		b.WriteString(renderRow(row, loc, tzLabel))
	}

	if card.Extra != "" {
		b.WriteString("  " + mutedStyle.Render(card.Extra) + "\n")
	}

	updated := "updated " + card.FetchedAt
	if card.Error != "" {
		updated += " — showing last good data"
	}
	b.WriteString("  " + mutedStyle.Render(updated) + "\n")
	return b.String()
}

func renderRow(row view.Row, loc *time.Location, tzLabel string) string {
	// Recompute time-relative fields from the ISO timestamp so they stay live
	// between backend pushes.
	resetsIn := row.ResetsIn
	if row.ResetsAtISO != "" {
		if t, err := time.Parse(time.RFC3339, row.ResetsAtISO); err == nil {
			resetsIn = format.ResetsIn(t)
			if loc != nil {
				resetsIn += " · " + resetClock(t, time.Now(), loc, tzLabel)
			}
		}
	}
	return fmt.Sprintf("  %-10s %s %3s   %s\n",
		row.Label,
		renderBar(row.Value),
		row.Pct,
		mutedStyle.Render(resetsIn),
	)
}

// resetClock formats the absolute reset time relative to now, both in loc.
// Same calendar day → time only ("4pm" or "4:10pm").
// Different day    → date + time ("Jun 15, 4am" or "Jun 15, 4:10pm").
// Minutes are omitted when zero.
func resetClock(resetsAt, now time.Time, loc *time.Location, tzLabel string) string {
	local := resetsAt.In(loc)
	nowLocal := now.In(loc)
	sameDay := local.Year() == nowLocal.Year() && local.YearDay() == nowLocal.YearDay()
	var layout string
	if sameDay {
		layout = "3:04:05pm"
	} else {
		layout = "Jan 2, 3:04:05pm"
	}
	return local.Format(layout) + " (" + tzLabel + ")"
}

func renderBar(value int) string {
	pct := float64(value)
	filled := int(pct/100*float64(barWidth) + 0.5)
	filled = min(max(filled, 0), barWidth)
	style := levelStyles[format.LevelOf(pct)]
	return style.Render(strings.Repeat("█", filled)) +
		emptyStyle.Render(strings.Repeat("░", barWidth-filled))
}
