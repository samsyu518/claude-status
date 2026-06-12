// Package tui renders the per-account usage dashboard in the terminal using
// bubbletea. It reads from a Backend, which transparently is either the
// in-process host store or a remote client — and may hand the host role over
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
	"go-gin-claude-status/internal/store"
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
	m := model{backend: b, snaps: b.Snapshots(), logs: logs, loc: loc, tzLabel: tzLabel}
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
	backend  Backend
	snaps    []store.Snapshot
	width    int
	logs     LogReader
	logLines []string
	loc      *time.Location
	tzLabel  string
}

type tickMsg time.Time

func tick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m model) Init() tea.Cmd { return tick() }

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tickMsg:
		m.snaps = m.backend.Snapshots()
		if m.logs != nil {
			m.logLines = m.logs.Lines()
		}
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
	b.WriteString(titleStyle.Render("claude-status") +
		subTypeStyle.Render(" ["+m.backend.Mode()+"]") +
		mutedStyle.Render("  ·  q to quit") + "\n\n")

	if len(m.snaps) == 0 {
		b.WriteString(mutedStyle.Render("no accounts yet…") + "\n")
		return b.String()
	}

	for _, s := range m.snaps {
		b.WriteString(renderAccount(s, m.loc, m.tzLabel))
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

func renderAccount(s store.Snapshot, loc *time.Location, tzLabel string) string {
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

	b.WriteString(renderRow("5h", s.FiveHour, loc, tzLabel))
	b.WriteString(renderRow("7d", s.SevenDay, loc, tzLabel))
	b.WriteString(renderRow("7d Opus", s.SevenDayOpus, loc, tzLabel))
	b.WriteString(renderRow("7d Sonnet", s.SevenDaySonnet, loc, tzLabel))

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

func renderRow(label string, w *store.Window, loc *time.Location, tzLabel string) string {
	if w == nil {
		return ""
	}
	resetsIn := format.ResetsIn(w.ResetsAt)
	if loc != nil && !w.ResetsAt.IsZero() {
		local := w.ResetsAt.In(loc)
		now := time.Now().In(loc)
		sameDay := local.Year() == now.Year() && local.YearDay() == now.YearDay()
		var layout string
		switch {
		case sameDay && local.Minute() == 0:
			layout = "3pm"
		case sameDay:
			layout = "3:04pm"
		case local.Minute() == 0:
			layout = "Jan 2, 3pm"
		default:
			layout = "Jan 2, 3:04pm"
		}
		resetsIn += " · " + local.Format(layout) + " (" + tzLabel + ")"
	}
	return fmt.Sprintf("  %-10s %s %3.0f%%   %s\n",
		label,
		renderBar(w.Utilization),
		w.Utilization,
		mutedStyle.Render(resetsIn),
	)
}

func renderBar(pct float64) string {
	filled := int(pct/100*float64(barWidth) + 0.5)
	filled = min(max(filled, 0), barWidth)
	style := levelStyles[format.LevelOf(pct)]
	return style.Render(strings.Repeat("█", filled)) +
		emptyStyle.Render(strings.Repeat("░", barWidth-filled))
}
