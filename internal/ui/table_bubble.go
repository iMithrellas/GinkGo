package ui

import (
	"context"
	"fmt"

	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/mithrel/ginkgo/pkg/api"
)

// RenderEntriesTable opens an interactive Bubble Tea table to browse entries.
func RenderEntriesTable(_ context.Context, entries []api.Entry) error {
	cols := []table.Column{
		{Title: "ID", Width: 14},
		{Title: "Title", Width: 40},
		{Title: "Tags", Width: 20},
		{Title: "Created", Width: 19},
	}

	rows := make([]table.Row, 0, len(entries))
	for _, e := range entries {
		created := e.CreatedAt.Local().Format("2006-01-02 15:04")
		rows = append(rows, table.Row{
			shortID(e.ID),
			truncate(e.Title, 40),
			truncate(joinTags(e.Tags), 20),
			created,
		})
	}

	t := table.New(
		table.WithColumns(cols),
		table.WithRows(rows),
		table.WithFocused(true),
		table.WithHeight(min(12, max(3, len(rows)+3))),
	)

	// Basic styling
	s := table.DefaultStyles()
	s.Header = s.Header.
		BorderStyle(lipgloss.NormalBorder()).
		BorderForeground(lipgloss.Color("240")).
		BorderBottom(true).
		Bold(true)
	s.Selected = s.Selected.
		Foreground(lipgloss.Color("229")).
		Background(lipgloss.Color("57")).
		Bold(false)
	t.SetStyles(s)

	m := model{table: t, entries: entries, showIdx: -1}
	p := tea.NewProgram(m, tea.WithAltScreen())
	final, err := p.Run()
	if err != nil {
		return err
	}
	if fm, ok := final.(model); ok {
		if fm.showIdx >= 0 && fm.showIdx < len(entries) {
			fmt.Print(FormatEntry(entries[fm.showIdx]))
		}
	}
	return nil
}

type model struct {
	table   table.Model
	entries []api.Entry
	showIdx int
}

func (m model) Init() tea.Cmd { return nil }

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "esc", "ctrl+c":
			return m, tea.Quit
		case "enter":
			// Mark selection to show after program exits, then quit.
			idx := m.table.Cursor()
			if idx >= 0 && idx < len(m.entries) {
				m.showIdx = idx
			}
			return m, tea.Quit
		}
	}
	var cmd tea.Cmd
	m.table, cmd = m.table.Update(msg)
	return m, cmd
}

func (m model) View() string {
	if m.table.Height() < 3 {
		return "(no entries)\n"
	}
	return m.table.View() + "\n↑/↓ to navigate • enter/q to exit\n"
}

func joinTags(tags []string) string {
	if len(tags) == 0 {
		return ""
	}
	// join with commas without importing strings in both files
	s := tags[0]
	for i := 1; i < len(tags); i++ {
		s += ", " + tags[i]
	}
	return s
}

func truncate(s string, n int) string {
	if n <= 3 || len(s) <= n {
		if len(s) > n {
			return s[:n]
		}
		return s
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func shortID(id string) string {
	if len(id) <= 12 {
		return id
	}
	return id[:12]
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
