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
	m := model{entries: entries, showIdx: -1}
	m.initTable()

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

func (m *model) initTable() {
	// Create table with default columns first
	defaultCols := []table.Column{
		{Title: "ID", Width: 12},
		{Title: "Title", Width: 40},
		{Title: "Tags", Width: 20},
		{Title: "Created", Width: 19},
	}

	m.table = table.New(
		table.WithColumns(defaultCols),
		table.WithFocused(true),
	)

	// Set default widths for initial row building
	m.titleWidth = 40
	m.tagsWidth = 20

	m.updateRows()
	m.applyStyles()
}

func (m *model) updateRows() {
	rows := make([]table.Row, 0, len(m.entries))
	for _, e := range m.entries {
		created := e.CreatedAt.Local().Format("2006-01-02 15:04")
		rows = append(rows, table.Row{
			shortID(e.ID),
			truncate(e.Title, m.titleWidth),
			truncate(joinTags(e.Tags), m.tagsWidth),
			created,
		})
	}
	m.table.SetRows(rows)
}

type model struct {
	table      table.Model
	entries    []api.Entry
	showIdx    int
	width      int
	height     int
	titleWidth int
	tagsWidth  int
}

func (m model) Init() tea.Cmd { return nil }

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.applyLayout()
		m.updateRows()
		return m, nil
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

func (m *model) applyLayout() {
	if m.width <= 0 || m.height <= 0 {
		return
	}
	// Leave one line footer; keep a minimal height of 6 rows
	h := max(6, m.height-1)
	m.table.SetHeight(h)
	m.table.SetWidth(m.width)

	// Compute column widths based on terminal width
	pad := 4 // borders/padding allowance
	avail := m.width - pad
	if avail < 40 {
		return // too narrow; keep defaults
	}

	idW := 12
	createdW := 19

	// Allocate remaining space proportionally
	rem := avail - idW - createdW
	if rem < 20 {
		rem = 20
	}

	// TODO : make these proportions configurable?
	tagsW := rem / 2
	titleW := rem - tagsW

	// Enforce minimal widths for usability
	if titleW < 8 {
		titleW = 8
	}
	if tagsW < 8 {
		tagsW = 8
	}

	m.titleWidth = titleW
	m.tagsWidth = tagsW

	cols := []table.Column{
		{Title: "ID", Width: idW},
		{Title: "Title", Width: titleW},
		{Title: "Tags", Width: tagsW},
		{Title: "Created", Width: createdW},
	}
	m.table.SetColumns(cols)
}

func (m *model) applyStyles() {
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
	m.table.SetStyles(s)
}
