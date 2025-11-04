package tui

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	overlay "github.com/rmhubbert/bubbletea-overlay"

	"github.com/mithrel/ginkgo/internal/editor"
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
	editIdx      int
	showModal    bool
	modal        *noteModal
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
	case showNoteResultMsg:
		// Full note fetch completed
		if msg.err != nil {
			if m.modal != nil {
				m.modal.setContent("Failed to load note: " + msg.err.Error())
			}
			m.status = "Load failed"
			m.lastDuration = msg.dur
			return m, nil
		}
		if msg.entry != nil && m.modal != nil {
			m.modal.setEntry(*msg.entry)
			m.status = "Loaded note"
			m.lastDuration = msg.dur
		}
		return m, nil
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
	case editResultMsg:
		if msg.err != nil {
			m.status = fmt.Sprintf("Edit failed: %v", msg.err)
			m.lastDuration = msg.dur
			m.editIdx = -1
			return m, nil
		}
		// If updated is nil, consider it a no-op
		if msg.updated != nil && msg.idx >= 0 && msg.idx < len(m.entries) {
			m.entries[msg.idx] = *msg.updated
			m.updateRows()
			m.table.SetCursor(msg.idx)
			m.status = fmt.Sprintf("Saved %s", msg.id)
		} else {
			m.status = "No changes."
		}
		m.lastDuration = msg.dur
		m.editIdx = -1
		return m, nil
	case editPrepMsg:
		// Launch external editor and handle save on completion
		mp := msg // capture for closure
		var cmd *exec.Cmd
		if mp.useShell {
			cmd = exec.Command("sh", "-c", "$EDITORCMD \"$FILEPATH\"")
			cmd.Env = append(os.Environ(), "EDITORCMD="+mp.shellCmd, "FILEPATH="+mp.path)
		} else {
			cmd = exec.Command(mp.editorPath, mp.path)
		}
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return m, tea.ExecProcess(cmd, func(err error) tea.Msg {
			res := editResultMsg{idx: mp.idx, id: mp.id}
			if err != nil {
				res.err = err
				res.dur = time.Since(mp.start)
				return res
			}
			out, rerr := os.ReadFile(mp.path)
			if rerr != nil {
				res.err = rerr
				res.dur = time.Since(mp.start)
				return res
			}
			if bytes.Equal(out, mp.initial) {
				// Editor returned without changes. If it returned very quickly,
				// it's likely a GUI editor without a wait flag. Keep temp file
				// so the user doesn't lose the buffer and show a helpful hint.
				dur := time.Since(mp.start)
				if dur < 500*time.Millisecond {
					res.err = fmt.Errorf("editor exited immediately; set $VISUAL/$EDITOR to include a wait flag (e.g., 'code --wait'). Temp file kept at %s", mp.path)
				} else {
					_ = os.Remove(mp.path)
				}
				res.dur = dur
				return res
			}
			// Changes detected; remove temp after reading
			_ = os.Remove(mp.path)
			title, tags, body := editor.ParseEditedNote(string(out))
			if title == "" && strings.TrimSpace(body) == "" {
				res.dur = time.Since(mp.start)
				return res
			}
			if title == "" {
				title = editor.FirstLine(body)
			}
			save, serr := ipc.Request(mp.ctx, mp.sock, ipc.Message{
				Name:      "note.edit",
				ID:        mp.curID,
				IfVersion: mp.curVersion,
				Title:     title,
				Body:      body,
				Tags:      tags,
			})
			if serr != nil {
				res.err = serr
				res.dur = time.Since(mp.start)
				return res
			}
			if !save.OK || save.Entry == nil {
				if save.Msg != "" {
					res.err = fmt.Errorf("%s", save.Msg)
				} else {
					res.err = fmt.Errorf("edit failed")
				}
				res.dur = time.Since(mp.start)
				return res
			}
			e := *save.Entry
			res.updated = &e
			res.dur = time.Since(mp.start)
			return res
		})
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		if m.showModal && m.modal != nil {
			var cmd tea.Cmd
			m.modal, cmd = m.modal.update(msg)
			_ = cmd
		}
		m.applyLayout()
		m.updateRows()
		return m, nil
	case tea.KeyMsg:
		if m.showModal && m.modal != nil {
			switch msg.String() {
			case "q", "esc", "enter", "i":
				m.showModal = false
				return m, nil
			default:
				var cmd tea.Cmd
				m.modal, cmd = m.modal.update(msg)
				return m, cmd
			}
		}
		switch msg.String() {
		case "q", "esc", "ctrl+c", "ctrl+q":
			if m.showModal {
				m.showModal = false
				return m, nil
			}
			return m, tea.Quit
		case "enter":
			if m.showModal {
				m.showModal = false
				return m, nil
			}
			idx := m.table.Cursor()
			if idx >= 0 && idx < len(m.entries) {
				m.showIdx = idx
			}
			return m, tea.Quit
		case "i":
			// Toggle content modal for the selected entry (rendered + scrollable)
			if len(m.entries) == 0 {
				return m, nil
			}
			if !m.showModal {
				idx := m.table.Cursor()
				if idx < 0 || idx >= len(m.entries) {
					idx = 0
				}
				sel := m.entries[idx]
				m.modal = newNoteModal(sel, m.width, m.height)
				m.showModal = true
				m.status = "Loading note…"
				m.lastDuration = 0
				return m, showNoteCmd(m.ctx, sel.ID)
			} else {
				m.showModal = false
			}
			return m, nil
		case "e":
			if m.editIdx >= 1 || m.deleteIdx >= 1 {
				// another operation in progress
				return m, nil
			}
			idx := m.table.Cursor()
			if idx >= 0 && idx < len(m.entries) {
				m.editIdx = idx
				sel := m.entries[idx]
				m.status = fmt.Sprintf("Editing %s…", sel.ID)
				return m, editCmd(m.ctx, sel.ID, idx)
			}
			return m, nil
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
	left := "↑/↓ to navigate • enter=show • d=delete • q=exit • e=edit • i=inspect"

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
		base := "(no entries) \n"
		if m.showModal && m.modal != nil {
			return m.renderWithOverlay(base)
		}
		return base
	}

	base := m.table.View() + "\n" + m.renderFooter() + "\n"
	if m.showModal && m.modal != nil {
		return m.renderWithOverlay(base)
	}
	return base
}

// renderWithOverlay composes a centered modal on top of the given base view string.
func (m model) renderWithOverlay(base string) string {
	// Background model simply renders the current base string.
	bg := simpleViewModel{view: base}
	// Foreground modal renders selected entry content (scrollable)
	fg := m.modal
	ov := overlay.New(fg, &bg, overlay.Center, overlay.Center, 0, 0)
	return ov.View()
}

// simpleViewModel adapts a raw string to a tea.Model to act as overlay background.
type simpleViewModel struct{ view string }

func (s *simpleViewModel) Init() tea.Cmd                           { return nil }
func (s *simpleViewModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) { return s, nil }
func (s *simpleViewModel) View() string                            { return s.view }

// noteModal is a foreground modal showing the full rendered note
// using Glamour inside a scrollable viewport.
type noteModal struct {
	e       api.Entry
	vp      viewport.Model
	width   int
	height  int
	padX    int
	padY    int
	box     lipgloss.Style
	content string
}

func newNoteModal(e api.Entry, termW, termH int) *noteModal {
	m := &noteModal{e: e, padX: 2, padY: 1}
	m.resizeForTerm(termW, termH)
	m.setContent("Loading…")
	return m
}

func (m *noteModal) resizeForTerm(termW, termH int) {
	if termW <= 0 || termH <= 0 {
		termW, termH = 80, 24
	}
	// 60% width, or nearly full width if terminal is small (<80 cols)
	w := int(float64(termW) * 0.6)
	if termW < 80 {
		w = termW - 4
	}
	if w < 40 {
		w = max(32, termW-2)
	}
	h := int(float64(termH) * 0.7)
	if termH < 20 {
		h = termH - 2
	}
	if h < 10 {
		h = max(8, termH-1)
	}
	m.width, m.height = w, h
	m.box = lipgloss.NewStyle().
		Width(w).
		Height(h).
		Padding(m.padY, m.padX).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("63"))

	innerW := w - 2 - m.padX*2 // borders + padding
	innerH := h - 2 - m.padY*2
	if innerW < 10 {
		innerW = 10
	}
	if innerH < 5 {
		innerH = 5
	}
	if m.vp.Width == 0 {
		m.vp = viewport.New(innerW, innerH)
	} else {
		m.vp.Width = innerW
		m.vp.Height = innerH
	}
	// Re-apply current content with new size
	m.vp.SetContent(m.content)
}

// setEntry renders the entry using the shared pretty renderer and sets it.
func (m *noteModal) setEntry(e api.Entry) {
	m.e = e
	var buf bytes.Buffer
	// Use the project renderer to avoid duplicating styling
	_ = format.WritePrettyEntry(&buf, e)
	m.setContent(buf.String())
}

func (m *noteModal) setContent(s string) {
	m.content = s
	m.vp.SetContent(s)
}

func (m *noteModal) update(msg tea.Msg) (*noteModal, tea.Cmd) {
	switch x := msg.(type) {
	case tea.WindowSizeMsg:
		m.resizeForTerm(x.Width, x.Height)
		return m, nil
	case tea.KeyMsg:
		var cmd tea.Cmd
		m.vp, cmd = m.vp.Update(msg)
		return m, cmd
	default:
		return m, nil
	}
}

func (m *noteModal) Init() tea.Cmd                           { return nil }
func (m *noteModal) Update(msg tea.Msg) (tea.Model, tea.Cmd) { return m.update(msg) }
func (m *noteModal) View() string                            { return m.box.Render(m.vp.View()) }

// showNoteResultMsg conveys the result of fetching a full note.
type showNoteResultMsg struct {
	entry *api.Entry
	err   error
	dur   time.Duration
}

// showNoteCmd fetches the full note via IPC and returns a result message.
func showNoteCmd(ctx context.Context, id string) tea.Cmd {
	return func() tea.Msg {
		start := time.Now()
		sock, err := ipc.SocketPath()
		if err != nil {
			return showNoteResultMsg{err: err}
		}
		resp, err := ipc.Request(ctx, sock, ipc.Message{Name: "note.show", ID: id})
		dur := time.Since(start)
		if err != nil {
			return showNoteResultMsg{err: err, dur: dur}
		}
		if !resp.OK || resp.Entry == nil {
			if resp.Msg != "" {
				return showNoteResultMsg{err: fmt.Errorf("%s", resp.Msg), dur: dur}
			}
			return showNoteResultMsg{err: fmt.Errorf("not found"), dur: dur}
		}
		e := *resp.Entry
		return showNoteResultMsg{entry: &e, dur: dur}
	}
}

// deleteResultMsg conveys the outcome of a delete operation back to Update.
type deleteResultMsg struct {
	idx int
	id  string
	err error
	dur time.Duration
}

// editResultMsg conveys the outcome of an edit operation back to Update.
type editResultMsg struct {
	idx     int
	id      string
	updated *api.Entry
	err     error
	dur     time.Duration
}

// editPrepMsg signals that the editor should be launched.
type editPrepMsg struct {
	ctx        context.Context
	idx        int
	id         string
	path       string
	initial    []byte
	editorPath string
	useShell   bool
	shellCmd   string
	curID      string
	curVersion int64
	sock       string
	start      time.Time
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

// editCmd opens the editor suspended and saves changes via IPC.
func editCmd(ctx context.Context, id string, idx int) tea.Cmd {
	return func() tea.Msg {
		start := time.Now()
		// Resolve socket
		sock, err := ipc.SocketPath()
		if err != nil {
			return editResultMsg{idx: idx, id: id, err: err, dur: time.Since(start)}
		}
		// Load latest entry
		show, err := ipc.Request(ctx, sock, ipc.Message{Name: "note.show", ID: id})
		if err != nil {
			return editResultMsg{idx: idx, id: id, err: err, dur: time.Since(start)}
		}
		if !show.OK || show.Entry == nil {
			if show.Msg != "" {
				return editResultMsg{idx: idx, id: id, err: fmt.Errorf("%s", show.Msg), dur: time.Since(start)}
			}
			return editResultMsg{idx: idx, id: id, err: fmt.Errorf("not found"), dur: time.Since(start)}
		}
		cur := *show.Entry
		path, err := editor.PathForID(cur.ID)
		if err != nil {
			return editResultMsg{idx: idx, id: id, err: err, dur: time.Since(start)}
		}
		initial := []byte(editor.ComposeContent(cur.Title, cur.Tags, cur.Body))
		if err := editor.PrepareAt(path, initial); err != nil {
			return editResultMsg{idx: idx, id: id, err: err, dur: time.Since(start)}
		}
		// Determine how to launch the editor. If VISUAL/EDITOR is set, use a shell
		// so flags like "--wait" are honored. Otherwise, fallback to preferred editor.
		vis := os.Getenv("VISUAL")
		if vis == "" {
			vis = os.Getenv("EDITOR")
		}
		if strings.TrimSpace(vis) != "" {
			return editPrepMsg{
				ctx:        ctx,
				idx:        idx,
				id:         id,
				path:       path,
				initial:    initial,
				useShell:   true,
				shellCmd:   vis,
				curID:      cur.ID,
				curVersion: cur.Version,
				sock:       sock,
				start:      start,
			}
		}
		ed, err := editor.PreferredEditor()
		if err != nil {
			return editResultMsg{idx: idx, id: id, err: err, dur: time.Since(start)}
		}
		return editPrepMsg{
			ctx:        ctx,
			idx:        idx,
			id:         id,
			path:       path,
			initial:    initial,
			editorPath: ed,
			curID:      cur.ID,
			curVersion: cur.Version,
			sock:       sock,
			start:      start,
		}
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
