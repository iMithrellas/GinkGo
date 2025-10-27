package tui

import (
	"context"
	"os"

	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/mithrel/ginkgo/internal/ipc"
	"github.com/mithrel/ginkgo/internal/present/format"
	"github.com/mithrel/ginkgo/pkg/api"
)

// RenderTable opens an interactive Bubble Tea table to browse entries.
func RenderTable(ctx context.Context, entries []api.Entry) error {
	m := model{entries: entries, showIdx: -1}
	m.initTable()

	p := tea.NewProgram(m, tea.WithAltScreen())
	final, err := p.Run()
	if err != nil {
		return err
	}
	if fm, ok := final.(model); ok {
		if fm.showIdx >= 0 && fm.showIdx < len(entries) {
			sel := entries[fm.showIdx]
			if sock, err := ipc.SocketPath(); err == nil {
				// Let daemon resolve namespace; only ID is required here.
				if resp, err := ipc.Request(ctx, sock, ipc.Message{Name: "note.show", ID: sel.ID}); err == nil && resp.OK && resp.Entry != nil {
					_ = format.WritePrettyEntry(os.Stdout, *resp.Entry)
				} else {
					_ = format.WritePrettyEntry(os.Stdout, sel)
				}
			} else {
				_ = format.WritePrettyEntry(os.Stdout, sel)
			}
		}
	}
	return nil
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

func (m *model) initTable() {
	defaultCols := []table.Column{
		{Title: "ID", Width: 12},
		{Title: "Title", Width: 40},
		{Title: "Tags", Width: 20},
		{Title: "Created", Width: 19},
	}
	m.table = table.New(table.WithColumns(defaultCols), table.WithFocused(true))
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

func (m *model) applyLayout() {
	if m.width <= 0 || m.height <= 0 {
		return
	}
	h := max(6, m.height-1)
	m.table.SetHeight(h)
	m.table.SetWidth(m.width)
	pad := 4
	avail := m.width - pad
	if avail < 40 {
		return
	}
	idW := 12
	createdW := 19
	rem := avail - idW - createdW
	if rem < 20 {
		rem = 20
	}
	tagsW := rem / 2
	titleW := rem - tagsW
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

func joinTags(tags []string) string {
	if len(tags) == 0 {
		return ""
	}
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

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
