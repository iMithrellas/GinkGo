package tui

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/mithrel/ginkgo/internal/editor"
	"github.com/mithrel/ginkgo/internal/ipc"
	"github.com/mithrel/ginkgo/pkg/api"
)

// showNoteResultMsg conveys the result of fetching a full note.
type showNoteResultMsg struct {
	entry *api.Entry
	err   error
	dur   time.Duration
}

// manualSyncResultMsg conveys the result of a manual sync trigger.
type manualSyncResultMsg struct {
	err error
	dur time.Duration
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

// windowResultMsg conveys the outcome of loading a centered window.
type windowResultMsg struct {
	entries      []api.Entry
	anchorID     string
	anchorIdx    int
	canFetchPrev bool
	canFetchNext bool
	status       string
	err          error
	dur          time.Duration
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

// showNoteCmd fetches the full note via IPC and returns a result message.
func showNoteCmd(ctx context.Context, id, namespace string) tea.Cmd {
	return func() tea.Msg {
		start := time.Now()
		sock, err := ipc.SocketPath()
		if err != nil {
			return showNoteResultMsg{err: err}
		}
		resp, err := ipc.Request(ctx, sock, ipc.Message{Name: "note.show", ID: id, Namespace: namespace})
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

func manualSyncCmd(ctx context.Context) tea.Cmd {
	return func() tea.Msg {
		start := time.Now()
		sock, err := ipc.SocketPath()
		if err != nil {
			return manualSyncResultMsg{err: err, dur: time.Since(start)}
		}
		resp, err := ipc.Request(ctx, sock, ipc.Message{Name: "sync.run"})
		if err != nil {
			return manualSyncResultMsg{err: err, dur: time.Since(start)}
		}
		if !resp.OK {
			return manualSyncResultMsg{err: fmt.Errorf("sync failed: %s", resp.Msg), dur: time.Since(start)}
		}
		return manualSyncResultMsg{err: nil, dur: time.Since(start)}
	}
}

func windowCmd(ctx context.Context, namespace string, tagsAny, tagsAll []string, since, until string, anchor api.Entry, wantBefore, wantAfter int, status string) tea.Cmd {
	return func() tea.Msg {
		start := time.Now()
		if wantBefore < 0 {
			wantBefore = 0
		}
		if wantAfter < 0 {
			wantAfter = 0
		}
		if wantBefore == 0 && wantAfter == 0 {
			wantAfter = 1
		}
		sock, err := ipc.SocketPath()
		if err != nil {
			return windowResultMsg{err: err, dur: time.Since(start)}
		}
		limit := wantBefore + wantAfter + 1
		if anchor.ID == "" {
			resp, err := ipc.Request(ctx, sock, ipc.Message{
				Name:      "note.list",
				Namespace: namespace,
				TagsAny:   tagsAny,
				TagsAll:   tagsAll,
				Since:     since,
				Until:     until,
				Limit:     limit,
			})
			if err != nil {
				return windowResultMsg{err: err, dur: time.Since(start)}
			}
			if !resp.OK {
				return windowResultMsg{err: fmt.Errorf("list failed: %s", resp.Msg), dur: time.Since(start)}
			}
			return windowResultMsg{
				entries:      resp.Entries,
				anchorID:     "",
				anchorIdx:    0,
				canFetchPrev: false,
				canFetchNext: resp.Page.Next != "",
				status:       status,
				dur:          time.Since(start),
			}
		}
		cursor := encodeCursor(anchor)
		newer, err := ipc.Request(ctx, sock, ipc.Message{
			Name:      "note.list",
			Namespace: namespace,
			TagsAny:   tagsAny,
			TagsAll:   tagsAll,
			Since:     since,
			Until:     until,
			Limit:     wantBefore,
			Cursor:    cursor,
			Reverse:   true,
		})
		if err != nil {
			return windowResultMsg{err: err, dur: time.Since(start)}
		}
		if !newer.OK {
			return windowResultMsg{err: fmt.Errorf("list failed: %s", newer.Msg), dur: time.Since(start)}
		}
		older, err := ipc.Request(ctx, sock, ipc.Message{
			Name:      "note.list",
			Namespace: namespace,
			TagsAny:   tagsAny,
			TagsAll:   tagsAll,
			Since:     since,
			Until:     until,
			Limit:     wantAfter,
			Cursor:    cursor,
			Reverse:   false,
		})
		if err != nil {
			return windowResultMsg{err: err, dur: time.Since(start)}
		}
		if !older.OK {
			return windowResultMsg{err: fmt.Errorf("list failed: %s", older.Msg), dur: time.Since(start)}
		}
		entries := make([]api.Entry, 0, len(newer.Entries)+1+len(older.Entries))
		entries = append(entries, newer.Entries...)
		entries = append(entries, anchor)
		entries = append(entries, older.Entries...)
		return windowResultMsg{
			entries:      entries,
			anchorID:     anchor.ID,
			anchorIdx:    len(newer.Entries),
			canFetchPrev: newer.Page.Prev != "",
			canFetchNext: older.Page.Next != "",
			status:       status,
			dur:          time.Since(start),
		}
	}
}

// deleteCmd deletes the entry with the given ID via IPC and reports the outcome.
// The command returns a deleteResultMsg containing the provided index and id, any error encountered, and the operation duration.
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
func editCmd(ctx context.Context, id, namespace string, idx int) tea.Cmd {
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
		path, err := editor.PathForID(cur.ID, namespace)
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