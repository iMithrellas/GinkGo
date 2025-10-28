package tui

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/mithrel/ginkgo/internal/ipc"
	"github.com/mithrel/ginkgo/internal/present/format"
	"github.com/mithrel/ginkgo/pkg/api"
)

// RenderTable opens an interactive Bubble Tea table to browse entries.
func RenderTable(ctx context.Context, entries []api.Entry, headers bool, initialStatus string, initialDuration time.Duration) error {
	m := model{
		ctx:          ctx,
		entries:      entries,
		showIdx:      -1,
		deleteIdx:    -1,
		headers:      headers,
		status:       initialStatus,
		lastDuration: initialDuration,
	}
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
	ctx          context.Context
	table        table.Model
	entries      []api.Entry
	showIdx      int
	deleteIdx    int
	headers      bool
	width        int
	height       int
	titleWidth   int
	tagsWidth    int
	status       string
	lastDuration time.Duration
}

func (m *model) initTable() {
	cols := m.columnsFor(m.headers, 12, 40, 20, 19)
	m.table = table.New(table.WithColumns(cols), table.WithFocused(true))
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
			e.ID,
			e.Title,
			joinTags(e.Tags),
			created,
		})
	}
	m.table.SetRows(rows)
}

func (m model) Init() tea.Cmd { return nil }

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case deleteResultMsg:
		// Handle delete completion
		if msg.err != nil {
			m.status = fmt.Sprintf("Delete failed: %v", msg.err)
			m.lastDuration = msg.dur
			m.deleteIdx = -1
			return m, nil
		}
		// Remove from entries if still valid
		if msg.idx >= 0 && msg.idx < len(m.entries) {
			m.entries = append(m.entries[:msg.idx], m.entries[msg.idx+1:]...)
		}
		// Rebuild rows and clamp cursor
		m.updateRows()
		newCur := msg.idx
		if newCur >= len(m.entries) {
			newCur = len(m.entries) - 1
		}
		if newCur < 0 {
			newCur = 0
		}
		m.table.SetCursor(newCur)
		m.status = fmt.Sprintf("Deleted %s", msg.id)
		m.lastDuration = msg.dur
		m.deleteIdx = -1
		return m, nil
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.applyLayout()
		m.updateRows()
		return m, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "esc", "ctrl+c", "ctrl+q":
			return m, tea.Quit
		case "enter":
			idx := m.table.Cursor()
			if idx >= 0 && idx < len(m.entries) {
				m.showIdx = idx
			}
			return m, tea.Quit
		case "d":
			idx := m.table.Cursor()
			if idx >= 0 && idx < len(m.entries) {
				m.deleteIdx = idx
				sel := m.entries[idx]
				m.status = fmt.Sprintf("Deleting %s…", sel.ID)
				return m, deleteCmd(m.ctx, sel.ID, idx)
			}
			return m, nil
		}
	}
	var cmd tea.Cmd
	m.table, cmd = m.table.Update(msg)
	return m, cmd
}

func (m model) renderFooter() string {
	left := "↑/↓ to navigate • enter=show • d=delete • q=exit"

	var right string
	if m.status != "" {
		if m.lastDuration > 0 {
			right = fmt.Sprintf("%s (%s) • ", m.status, m.lastDuration)
		} else {
			right = m.status + " • "
		}
	}
	right += fmt.Sprintf("%d entries ", len(m.entries))

	width := m.table.Width()
	space := width - lipgloss.Width(left) - lipgloss.Width(right)
	if space < 1 {
		space = 1
	}

	return left + strings.Repeat(" ", space) + right
}

func (m model) View() string {
	if m.table.Height() < 3 {
		return "(no entries) \n"
	}

	return m.table.View() + "\n" + m.renderFooter() + "\n"
}

// deleteResultMsg conveys the outcome of a delete operation back to Update.
type deleteResultMsg struct {
	idx int
	id  string
	err error
	dur time.Duration
}

// deleteCmd performs the IPC call to delete an entry and returns a deleteResultMsg.
func deleteCmd(ctx context.Context, id string, idx int) tea.Cmd {
	return func() tea.Msg {
		start := time.Now()
		sock, err := ipc.SocketPath()
		if err != nil {
			return deleteResultMsg{idx: idx, id: id, err: err}
		}
		resp, err := ipc.Request(ctx, sock, ipc.Message{Name: "note.delete", ID: id})
		if err != nil {
			return deleteResultMsg{idx: idx, id: id, err: err, dur: time.Since(start)}
		}
		if !resp.OK {
			if resp.Msg != "" {
				return deleteResultMsg{idx: idx, id: id, err: fmt.Errorf("%s", resp.Msg), dur: time.Since(start)}
			}
			return deleteResultMsg{idx: idx, id: id, err: fmt.Errorf("delete failed"), dur: time.Since(start)}
		}
		return deleteResultMsg{idx: idx, id: id, err: nil, dur: time.Since(start)}
	}
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
	minIDWidth := 8
	fullIDWidth := 26
	idW := fullIDWidth
	if avail < fullIDWidth+60 {
		idW = minIDWidth
	}
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
	cols := m.columnsFor(m.headers, idW, titleW, tagsW, createdW)
	m.table.SetColumns(cols)
}

func (m *model) applyStyles() {
	s := table.DefaultStyles()
	if m.headers {
		s.Header = s.Header.
			BorderStyle(lipgloss.NormalBorder()).
			BorderForeground(lipgloss.Color("240")).
			BorderBottom(true).
			Bold(true)
	} else {
		// Minimize header prominence when disabled
		s.Header = s.Header.
			BorderBottom(false).
			Bold(false)
	}
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

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// columnsFor returns columns with or without titles based on headers flag.
func (m *model) columnsFor(headers bool, idW, titleW, tagsW, createdW int) []table.Column {
	if headers {
		return []table.Column{
			{Title: "ID", Width: idW},
			{Title: "Title", Width: titleW},
			{Title: "Tags", Width: tagsW},
			{Title: "Created", Width: createdW},
		}
	}
	return []table.Column{
		{Title: "", Width: idW},
		{Title: "", Width: titleW},
		{Title: "", Width: tagsW},
		{Title: "", Width: createdW},
	}
}
