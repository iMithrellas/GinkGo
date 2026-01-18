package tui

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"golang.org/x/term"

	"github.com/mithrel/ginkgo/internal/editor"
	"github.com/mithrel/ginkgo/internal/ipc"
	"github.com/mithrel/ginkgo/internal/present/format"
	"github.com/mithrel/ginkgo/internal/util"
	"github.com/mithrel/ginkgo/pkg/api"
)

// RenderTable opens an interactive Bubble Tea table to browse entries.
func RenderTable(ctx context.Context, entries []api.Entry, headers bool, initialStatus string, initialDuration time.Duration, filterTagsAny, filterTagsAll, filterSince, filterUntil, namespace string, bufferRatio float64) error {
	m := model{
		ctx:          ctx,
		entries:      entries,
		showIdx:      -1,
		deleteIdx:    -1,
		headers:      headers,
		status:       initialStatus,
		lastDuration: initialDuration,
		tagsAny:      filterTagsAny,
		tagsAll:      filterTagsAll,
		since:        filterSince,
		until:        filterUntil,
		namespace:    namespace,
		bufferRatio:  bufferRatio,
	}
	m.initTable()
	if width, height, err := term.GetSize(int(os.Stdout.Fd())); err == nil && width > 0 && height > 0 {
		m.width = width
		m.height = height
		m.applyLayout()
	}

	p := tea.NewProgram(m, tea.WithAltScreen())
	final, err := p.Run()
	if err != nil {
		return err
	}
	if fm, ok := final.(model); ok {
		if fm.showIdx >= 0 && fm.showIdx < len(entries) {
			sel := entries[fm.showIdx]
			if sock, err := ipc.SocketPath(); err == nil {
				if resp, err := ipc.Request(ctx, sock, ipc.Message{Name: "note.show", ID: sel.ID, Namespace: sel.Namespace}); err == nil && resp.OK && resp.Entry != nil {
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
	ctx           context.Context
	table         table.Model
	entries       []api.Entry
	showIdx       int
	deleteIdx     int
	editIdx       int
	showHelp      bool
	help          help.Model
	keys          keyMap
	showModal     bool
	modal         *noteModal
	showFilter    bool
	filterModal   *filterModal
	headers       bool
	width         int
	height        int
	titleWidth    int
	tagsWidth     int
	status        string
	lastDuration  time.Duration
	tagsAny       string
	tagsAll       string
	since         string
	until         string
	namespace     string
	pageSize      int
	bufferSize    int
	bufferRatio   float64
	viewSize      int
	canFetchPrev  bool
	canFetchNext  bool
	loaded        bool
	loadingWindow bool
}

func (m *model) initTable() {
	m.keys = newKeyMap()
	m.help = help.New()
	m.help.ShowAll = true
	cols := m.columnsFor(m.headers, 12, 40, 20, 19)
	m.table = table.New(table.WithColumns(cols), table.WithFocused(true))
	m.titleWidth = 40
	m.tagsWidth = 20
	if m.pageSize == 0 {
		m.pageSize = 50
	}
	if m.bufferRatio == 0 {
		m.bufferRatio = 0.2
	}
	m.bufferRatio = clampBufferRatio(m.bufferRatio)
	m.updateWindowSize()
	m.loaded = len(m.entries) > 0
	m.updateRows(0)
	m.applyStyles()
	m.updateKeyStates()
}

func initialWindowSizeCmd() tea.Cmd {
	return func() tea.Msg {
		width, height, err := term.GetSize(int(os.Stdout.Fd()))
		if err != nil || width <= 0 || height <= 0 {
			width, height = 80, 24
		}
		return tea.WindowSizeMsg{Width: width, Height: height}
	}
}

func (m *model) updateRows(prepended int) {
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
	if prepended > 0 {
		m.table.SetRowsWithAnchor(rows, prepended)
	} else {
		m.table.SetRows(rows)
	}
}

func (m model) Init() tea.Cmd { return initialWindowSizeCmd() }

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	m.updateKeyStates()
	switch msg := msg.(type) {
	case showNoteResultMsg:
		// Full note fetch completed
		if msg.err != nil {
			if m.modal != nil {
				m.modal.setContent("Failed to load note: " + msg.err.Error())
			}
			m.status = "Load failed"
			m.lastDuration = msg.dur
			m.updateKeyStates()
			return m, nil
		}
		if msg.entry != nil && m.modal != nil {
			m.modal.setEntry(*msg.entry)
			m.status = "Loaded note"
			m.lastDuration = msg.dur
		}
		m.updateKeyStates()
		return m, nil
	case deleteResultMsg:
		// Handle delete completion
		if msg.err != nil {
			m.status = fmt.Sprintf("Delete failed: %v", msg.err)
			m.lastDuration = msg.dur
			m.deleteIdx = -1
			m.updateKeyStates()
			return m, nil
		}
		// Remove from entries if still valid
		if msg.idx >= 0 && msg.idx < len(m.entries) {
			m.entries = append(m.entries[:msg.idx], m.entries[msg.idx+1:]...)
		}
		// Rebuild rows and clamp cursor
		m.updateRows(0)
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
		m.updateKeyStates()
		return m, nil
	case editResultMsg:
		if msg.err != nil {
			m.status = fmt.Sprintf("Edit failed: %v", msg.err)
			m.lastDuration = msg.dur
			m.editIdx = -1
			m.updateKeyStates()
			return m, nil
		}
		// If updated is nil, consider it a no-op
		if msg.updated != nil && msg.idx >= 0 && msg.idx < len(m.entries) {
			m.entries[msg.idx] = *msg.updated
			m.updateRows(0)
			m.table.SetCursor(msg.idx)
			m.status = fmt.Sprintf("Saved %s", msg.id)
		} else {
			m.status = "No changes."
		}
		m.lastDuration = msg.dur
		m.editIdx = -1
		m.updateKeyStates()
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
	case manualSyncResultMsg:
		if msg.err != nil {
			m.status = fmt.Sprintf("Sync error: %v", msg.err)
		} else {
			m.status = "Sync triggered"
		}
		m.lastDuration = msg.dur
		m.updateKeyStates()
		return m, nil
	case windowResultMsg:
		if msg.err != nil {
			m.status = fmt.Sprintf("Load failed: %v", msg.err)
			m.lastDuration = msg.dur
			m.loadingWindow = false
			m.updateKeyStates()
			return m, nil
		}
		m.entries = msg.entries
		m.canFetchPrev = msg.canFetchPrev
		m.canFetchNext = msg.canFetchNext
		m.loaded = true
		anchorIdx := msg.anchorIdx
		if anchorIdx < 0 {
			if msg.anchorID != "" {
				anchorIdx = indexOfEntryID(m.entries, msg.anchorID)
			} else {
				anchorIdx = 0
			}
		}
		prepended := 0
		oldCursor := m.table.Cursor()
		if oldCursor >= 0 && anchorIdx >= 0 && anchorIdx > oldCursor {
			prepended = anchorIdx - oldCursor
		}
		m.updateRows(prepended)
		if anchorIdx >= 0 && anchorIdx < len(m.entries) {
			m.table.SetCursor(anchorIdx)
		} else {
			m.table.SetCursor(0)
		}
		if msg.status != "" {
			m.status = msg.status
		}
		m.lastDuration = msg.dur
		m.loadingWindow = false
		m.updateKeyStates()
		return m, nil
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		if m.showModal && m.modal != nil {
			var cmd tea.Cmd
			m.modal, cmd = m.modal.update(msg)
			_ = cmd
		}
		if m.showFilter && m.filterModal != nil {
			var cmd tea.Cmd
			m.filterModal, cmd = m.filterModal.update(msg)
			_ = cmd
		}
		m.applyLayout()
		m.updateRows(0)
		if !m.loaded && m.viewSize > 0 {
			m.status = "Loading..."
			side := m.windowSide()
			m.updateKeyStates()
			return m, windowCmd(m.ctx, m.namespace, splitCSV(m.tagsAny), splitCSV(m.tagsAll), m.since, m.until, api.Entry{}, side, side, "Loaded")
		}
		m.updateKeyStates()
		return m, nil
	case tea.KeyMsg:
		if m.showHelp {
			switch msg.String() {
			case "?", "q", "esc", "enter":
				m.showHelp = false
			}
			m.updateKeyStates()
			return m, nil
		}
		if m.showModal && m.modal != nil {
			switch msg.String() {
			case "q", "esc", "enter", "i", "I":
				m.showModal = false
				m.updateKeyStates()
				return m, nil
			default:
				var cmd tea.Cmd
				m.modal, cmd = m.modal.update(msg)
				m.updateKeyStates()
				return m, cmd
			}
		}
		if m.showFilter && m.filterModal != nil {
			switch msg.String() {
			case "esc", "ctrl+q":
				m.showFilter = false
				m.updateKeyStates()
				return m, nil
			case "ctrl+x":
				m.tagsAny = ""
				m.tagsAll = ""
				m.since = ""
				m.until = ""
				m.showFilter = false
				m.status = "Clearing filters..."
				m.loaded = false
				side := m.windowSide()
				m.updateKeyStates()
				return m, windowCmd(m.ctx, m.namespace, nil, nil, "", "", api.Entry{}, side, side, "Filters cleared")
			case "enter":
				tagsAny, tagsAll, since, until := m.filterModal.values()
				normalizedSince, normalizedUntil, err := util.NormalizeTimeRange(since, until)
				if err != nil {
					m.status = fmt.Sprintf("Filter error: %v", err)
					m.lastDuration = 0
					return m, nil
				}
				m.tagsAny = tagsAny
				m.tagsAll = tagsAll
				m.since = since
				m.until = until
				m.showFilter = false
				m.status = "Filtering..."
				m.loaded = false
				side := m.windowSide()
				m.updateKeyStates()
				return m, windowCmd(m.ctx, m.namespace, splitCSV(tagsAny), splitCSV(tagsAll), normalizedSince, normalizedUntil, api.Entry{}, side, side, "Filters applied")
			default:
				var cmd tea.Cmd
				m.filterModal, cmd = m.filterModal.update(msg)
				m.updateKeyStates()
				return m, cmd
			}
		}
		switch msg.String() {
		case "q", "esc", "ctrl+c", "ctrl+q":
			if m.showModal {
				m.showModal = false
				m.updateKeyStates()
				return m, nil
			}
			m.updateKeyStates()
			return m, tea.Quit
		case "?":
			if m.showModal || m.showFilter {
				m.updateKeyStates()
				return m, nil
			}
			m.showHelp = true
			m.updateKeyStates()
			return m, nil
		case "i", "I", "enter":
			// Toggle content modal for the selected entry (rendered + scrollable)
			if len(m.entries) == 0 {
				m.updateKeyStates()
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
				m.updateKeyStates()
				return m, showNoteCmd(m.ctx, sel.ID, sel.Namespace)
			} else {
				m.showModal = false
			}
			m.updateKeyStates()
			return m, nil
		case "e":
			if m.editIdx >= 1 || m.deleteIdx >= 1 {
				// another operation in progress
				m.updateKeyStates()
				return m, nil
			}
			idx := m.table.Cursor()
			if idx >= 0 && idx < len(m.entries) {
				m.editIdx = idx
				sel := m.entries[idx]
				m.status = fmt.Sprintf("Editing %s…", sel.ID)
				m.updateKeyStates()
				return m, editCmd(m.ctx, sel.ID, sel.Namespace, idx)
			}
			m.updateKeyStates()
			return m, nil
		case "s", "S":
			m.status = "Triggering sync..."
			m.updateKeyStates()
			return m, manualSyncCmd(m.ctx)
		case "f":
			if m.showModal {
				m.updateKeyStates()
				return m, nil
			}
			m.filterModal = newFilterModal(m.tagsAny, m.tagsAll, m.since, m.until, m.namespace, m.width, m.height)
			m.showFilter = true
			m.updateKeyStates()
			return m, nil
		case "d":
			idx := m.table.Cursor()
			if idx >= 0 && idx < len(m.entries) {
				m.deleteIdx = idx
				sel := m.entries[idx]
				m.status = fmt.Sprintf("Deleting %s…", sel.ID)
				m.updateKeyStates()
				return m, deleteCmd(m.ctx, sel.ID, idx)
			}
			m.updateKeyStates()
			return m, nil
		}
	}
	var cmd tea.Cmd
	m.table, cmd = m.table.Update(msg)
	m.updateKeyStates()
	if refetchCmd := m.maybeRefetchWindow(); refetchCmd != nil {
		return m, tea.Batch(cmd, refetchCmd)
	}
	return m, cmd
}

func (m model) renderFooter() string {
	left := " press ? for help"

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
	if m.showHelp {
		helpView, w, h := m.helpModalView()
		base := m.table.View() + "\n" + m.renderFooter()
		return m.renderOverlay(base, helpView, w, h)
	}
	if m.table.Height() < 3 {
		base := "(no entries)"
		if m.showModal && m.modal != nil {
			return m.renderOverlay(base, m.modal.View(), m.modal.width, m.modal.height)
		}
		if m.showFilter && m.filterModal != nil {
			return m.renderOverlay(base, m.filterModal.View(), m.filterModal.width, m.filterModal.height)
		}
		return base
	}

	base := m.table.View() + "\n" + m.renderFooter()
	if m.showModal && m.modal != nil {
		return m.renderOverlay(base, m.modal.View(), m.modal.width, m.modal.height)
	}
	if m.showFilter && m.filterModal != nil {
		return m.renderOverlay(base, m.filterModal.View(), m.filterModal.width, m.filterModal.height)
	}
	return base
}

func (m *model) applyLayout() {
	if m.width <= 0 || m.height <= 0 {
		return
	}
	h := max(6, m.height-1)
	m.table.SetHeight(h)
	m.table.SetWidth(m.width)
	m.pageSize = max(5, m.table.Height())
	m.updateWindowSize()
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
	// Re-apply header styles after column/size changes to avoid stale rendering.
	m.applyStyles()
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

func (m *model) updateWindowSize() {
	view := m.table.Height()
	if view <= 0 {
		view = m.pageSize
	}
	if view <= 0 {
		view = 10
	}
	m.viewSize = max(5, view)
	m.bufferSize = max(1, int(float64(m.viewSize)*m.bufferRatio))
}

func (m *model) updateKeyStates() {
	hasEntries := len(m.entries) > 0
	m.keys.Show.SetEnabled(hasEntries)
	m.keys.Edit.SetEnabled(hasEntries)
	m.keys.Delete.SetEnabled(hasEntries)
}

func (m model) helpModalView() (string, int, int) {
	content := m.help.View(m.keys)
	box := lipgloss.NewStyle().
		Padding(1, 2).
		Border(lipgloss.NormalBorder()).
		BorderForeground(lipgloss.Color("63"))
	view := box.Render(content)
	return view, lipgloss.Width(view), lipgloss.Height(view)
}

func (m *model) requiredSide() int {
	return m.viewSize + m.bufferSize
}

func (m *model) windowSide() int {
	required := m.requiredSide()
	slack := max(m.viewSize/2, m.bufferSize/2)
	if slack < 1 {
		slack = 1
	}
	return required + slack
}

func clampBufferRatio(r float64) float64 {
	if r < 0.4 {
		return 0.4
	}
	if r > 4 {
		return 4
	}
	return r
}

func (m *model) needsWindowRefetch() bool {
	if m.showModal || m.showFilter || m.loadingWindow {
		return false
	}
	if len(m.entries) == 0 {
		return false
	}
	cur := m.table.Cursor()
	if cur < 0 || cur >= len(m.entries) {
		return false
	}
	required := m.requiredSide()
	if required == 0 {
		return false
	}
	before := cur
	after := len(m.entries) - 1 - cur
	if before < required && m.canFetchPrev {
		return true
	}
	if after < required && m.canFetchNext {
		return true
	}
	return false
}

func (m *model) maybeRefetchWindow() tea.Cmd {
	if !m.needsWindowRefetch() {
		return nil
	}
	cur := m.table.Cursor()
	if cur < 0 || cur >= len(m.entries) {
		return nil
	}
	m.loadingWindow = true
	m.status = "Loading..."
	m.lastDuration = 0
	anchor := m.entries[cur]
	side := m.windowSide()
	return windowCmd(m.ctx, m.namespace, splitCSV(m.tagsAny), splitCSV(m.tagsAll), m.since, m.until, anchor, side, side, "Loaded window")
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

type keyMap struct {
	Up     key.Binding
	Down   key.Binding
	Show   key.Binding
	Edit   key.Binding
	Delete key.Binding
	Sync   key.Binding
	Filter key.Binding
	Help   key.Binding
	Quit   key.Binding
}

func newKeyMap() keyMap {
	return keyMap{
		Up: key.NewBinding(
			key.WithKeys("k", "up"),
			key.WithHelp("k/↑", "move up"),
		),
		Down: key.NewBinding(
			key.WithKeys("j", "down"),
			key.WithHelp("j/↓", "move down"),
		),
		Show: key.NewBinding(
			key.WithKeys("enter", "i"),
			key.WithHelp("enter/i", "inspect"),
		),
		Edit: key.NewBinding(
			key.WithKeys("e"),
			key.WithHelp("e", "edit"),
		),
		Delete: key.NewBinding(
			key.WithKeys("d"),
			key.WithHelp("d", "delete"),
		),
		Sync: key.NewBinding(
			key.WithKeys("s"),
			key.WithHelp("s", "sync now"),
		),
		Filter: key.NewBinding(
			key.WithKeys("f"),
			key.WithHelp("f", "filter"),
		),
		Help: key.NewBinding(
			key.WithKeys("?"),
			key.WithHelp("?", "help"),
		),
		Quit: key.NewBinding(
			key.WithKeys("q", "esc"),
			key.WithHelp("q/esc", "quit"),
		),
	}
}

func (k keyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Up, k.Down, k.Show, k.Edit, k.Delete, k.Help, k.Quit}
}

func (k keyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Up, k.Down, k.Show, k.Edit},
		{k.Delete, k.Filter, k.Sync},
		{k.Help, k.Quit},
	}
}
