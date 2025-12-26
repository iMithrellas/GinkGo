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
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

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
	showFilter   bool
	filterModal  *filterModal
	headers      bool
	width        int
	height       int
	titleWidth   int
	tagsWidth    int
	status       string
	lastDuration time.Duration
	tagsAny      string
	tagsAll      string
	since        string
	until        string
	namespace    string
	nextCursor   string
	prevCursor   string
	canFetchPrev bool
	lastPrevSent string
	pageSize     int
	bufferSize   int
	bufferRatio  float64
	loaded       bool
	loadingNext  bool
	loadingPrev  bool
}

type listMode int

const (
	listModeReplace listMode = iota
	listModeAppend
	listModePrepend
)

const scrollOffsetRows = 2

func (m *model) initTable() {
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
	m.updateBufferSize()
	m.loaded = len(m.entries) > 0
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
	case manualSyncResultMsg:
		if msg.err != nil {
			m.status = fmt.Sprintf("Sync error: %v", msg.err)
		} else {
			m.status = "Sync triggered"
		}
		m.lastDuration = msg.dur
		return m, nil
	case listResultMsg:
		if msg.err != nil {
			m.status = fmt.Sprintf("Filter failed: %v", msg.err)
			m.lastDuration = msg.dur
			if msg.mode == listModeAppend {
				m.loadingNext = false
			}
			if msg.mode == listModePrepend {
				m.loadingPrev = false
			}
			return m, nil
		}
		switch msg.mode {
		case listModeReplace:
			m.entries = msg.entries
			m.table.SetCursor(0)
			m.status = "Filters applied"
			m.nextCursor = msg.page.Next
			m.prevCursor = msg.page.Prev
			m.canFetchPrev = msg.page.Prev != ""
			m.lastPrevSent = ""
			m.loaded = true
		case listModeAppend:
			if len(msg.entries) == 0 {
				m.nextCursor = ""
			} else {
				m.entries = append(m.entries, msg.entries...)
				if msg.page.Next != "" {
					m.nextCursor = msg.page.Next
				}
				m.status = "Loaded more"
			}
			m.loadingNext = false
		case listModePrepend:
			if len(msg.entries) == 0 {
				m.prevCursor = ""
			} else {
				m.entries = append(msg.entries, m.entries...)
				m.table.SetCursor(m.table.Cursor() + len(msg.entries))
				m.status = "Loaded newer"
			}
			m.prevCursor = msg.page.Prev
			m.canFetchPrev = msg.page.Prev != ""
			m.loadingPrev = false
		}
		m.updateRows()
		m.lastDuration = msg.dur
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
		m.updateRows()
		if !m.loaded && m.pageSize > 0 {
			m.status = "Loading..."
			return m, listCmd(m.ctx, m.namespace, splitCSV(m.tagsAny), splitCSV(m.tagsAll), m.since, m.until, m.pageSize, "", false, listModeReplace)
		}
		return m, nil
	case tea.KeyMsg:
		if m.showModal && m.modal != nil {
			switch msg.String() {
			case "q", "esc", "enter", "i", "I":
				m.showModal = false
				return m, nil
			default:
				var cmd tea.Cmd
				m.modal, cmd = m.modal.update(msg)
				return m, cmd
			}
		}
		if m.showFilter && m.filterModal != nil {
			switch msg.String() {
			case "esc", "ctrl+q":
				m.showFilter = false
				return m, nil
			case "ctrl+x":
				m.tagsAny = ""
				m.tagsAll = ""
				m.since = ""
				m.until = ""
				m.showFilter = false
				m.status = "Clearing filters..."
				m.nextCursor = ""
				m.prevCursor = ""
				m.loaded = false
				return m, listCmd(m.ctx, m.namespace, nil, nil, "", "", m.pageSize, "", false, listModeReplace)
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
				m.nextCursor = ""
				m.prevCursor = ""
				m.loaded = false
				return m, listCmd(m.ctx, m.namespace, splitCSV(tagsAny), splitCSV(tagsAll), normalizedSince, normalizedUntil, m.pageSize, "", false, listModeReplace)
			default:
				var cmd tea.Cmd
				m.filterModal, cmd = m.filterModal.update(msg)
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
		case "i", "I":
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
		case "s", "S":
			m.status = "Triggering sync..."
			return m, manualSyncCmd(m.ctx)
		case "f":
			if m.showModal {
				return m, nil
			}
			m.filterModal = newFilterModal(m.tagsAny, m.tagsAll, m.since, m.until, m.namespace, m.width, m.height)
			m.showFilter = true
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
	if prevCmd := m.maybeFetchPrev(); prevCmd != nil {
		m.maybePrune()
		if nextCmd := m.maybeFetchNext(); nextCmd != nil {
			return m, tea.Batch(cmd, prevCmd, nextCmd)
		}
		return m, tea.Batch(cmd, prevCmd)
	}
	m.maybePrune()
	if nextCmd := m.maybeFetchNext(); nextCmd != nil {
		return m, tea.Batch(cmd, nextCmd)
	}
	return m, cmd
}

func (m model) renderFooter() string {
	left := "↑/↓ to navigate • enter=show • d=delete • q=exit • e=edit • i=inspect • s=sync • f=filter"

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
			return m.renderOverlay(base, m.modal.View(), m.modal.width, m.modal.height)
		}
		if m.showFilter && m.filterModal != nil {
			return m.renderOverlay(base, m.filterModal.View(), m.filterModal.width, m.filterModal.height)
		}
		return base
	}

	base := m.table.View() + "\n" + m.renderFooter() + "\n"
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
	m.updateBufferSize()
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

func (m *model) updateBufferSize() {
	if m.pageSize <= 0 {
		m.bufferSize = 0
		return
	}
	m.bufferSize = max(1, int(float64(m.pageSize)*m.bufferRatio))
}

func clampBufferRatio(r float64) float64 {
	if r < 0.1 {
		return 0.1
	}
	if r > 0.3 {
		return 0.3
	}
	return r
}

func (m *model) prefetchThreshold() int {
	if m.bufferSize == 0 {
		return 0
	}
	if scrollOffsetRows > m.bufferSize {
		return scrollOffsetRows
	}
	return m.bufferSize
}

func (m *model) maybeFetchNext() tea.Cmd {
	if m.showModal || m.showFilter {
		return nil
	}
	if m.loadingNext || m.nextCursor == "" {
		return nil
	}
	if len(m.entries) == 0 {
		return nil
	}
	threshold := m.prefetchThreshold()
	if threshold == 0 {
		return nil
	}
	if m.table.Cursor() < len(m.entries)-1-threshold {
		return nil
	}
	m.loadingNext = true
	return listCmd(m.ctx, m.namespace, splitCSV(m.tagsAny), splitCSV(m.tagsAll), m.since, m.until, m.pageSize, m.nextCursor, false, listModeAppend)
}

func (m *model) maybeFetchPrev() tea.Cmd {
	if m.showModal || m.showFilter {
		return nil
	}
	if m.loadingPrev || !m.canFetchPrev || m.prevCursor == "" {
		return nil
	}
	if m.prevCursor == m.lastPrevSent {
		return nil
	}
	threshold := m.prefetchThreshold()
	if len(m.entries) == 0 || threshold == 0 {
		return nil
	}
	if m.table.Cursor() > threshold {
		return nil
	}
	m.loadingPrev = true
	m.lastPrevSent = m.prevCursor
	return listCmd(m.ctx, m.namespace, splitCSV(m.tagsAny), splitCSV(m.tagsAll), m.since, m.until, m.pageSize, m.prevCursor, true, listModePrepend)
}

func (m *model) maybePrune() {
	if m.bufferSize == 0 || len(m.entries) == 0 {
		return
	}
	if m.loadingPrev {
		return
	}
	cur := m.table.Cursor()
	viewTop := max(0, cur-m.table.Height())
	drop := viewTop - m.bufferSize
	if drop <= 0 {
		return
	}
	if drop >= len(m.entries) {
		return
	}
	m.entries = m.entries[drop:]
	m.table.SetCursor(cur - drop)
	m.prevCursor = encodeCursor(m.entries[0])
	m.canFetchPrev = true
	m.updateRows()
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
