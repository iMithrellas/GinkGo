package cli

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/mithrel/ginkgo/internal/db"
	"github.com/mithrel/ginkgo/internal/ipc"
	"github.com/spf13/cobra"
)

func newNoteEditCmd() *cobra.Command {
	var keepTmp bool
	var force bool
	cmd := &cobra.Command{
		Use:   "edit <id>",
		Short: "Edit an existing note",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := getApp(cmd)
			id := args[0]
			sock, err := ipc.SocketPath()
			if err != nil {
				return err
			}
			show, err := ipc.Request(cmd.Context(), sock, ipc.Message{Name: "note.show", ID: id, Namespace: app.Cfg.Namespace})
			if err != nil {
				return err
			}
			if !show.OK || show.Entry == nil {
				if show.Msg != "" {
					return errors.New(show.Msg)
				}
				return errors.New("not found")
			}
			cur := *show.Entry
			// Prefill editor content
			var b bytes.Buffer
			b.WriteString("# GinkGo Note Edit\n")
			b.WriteString("# Update Title/Tags above, body below.\n")
			b.WriteString("Title: ")
			b.WriteString(cur.Title)
			b.WriteString("\n")
			b.WriteString("Tags: ")
			if len(cur.Tags) > 0 {
				b.WriteString(strings.Join(cur.Tags, ", "))
			}
			b.WriteString("\n---\n")
			if cur.Body != "" {
				b.WriteString(cur.Body)
			}

			path, err := editorPathForID(id)
			if err != nil {
				return err
			}
			out, changed, err := openEditorAt(path, []byte(b.String()))
			if err != nil {
				return err
			}
			if !keepTmp {
				_ = os.Remove(path)
			}
			if !changed {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "No changes.")
				return nil
			}
			title, tags, body := parseEditedNote(string(out))
			if title == "" && strings.TrimSpace(body) == "" {
				return fmt.Errorf("edit aborted: empty content")
			}
			if title == "" {
				title = firstLine(body)
				if title == "" {
					return fmt.Errorf("edit aborted: empty content")
				}
			}
			cur.Title = title
			cur.Tags = tags
			cur.Body = body
			cur.UpdatedAt = time.Now().UTC()
			eResp, err := ipc.Request(cmd.Context(), sock, ipc.Message{Name: "note.edit", ID: cur.ID, IfVersion: cur.Version, Title: cur.Title, Body: cur.Body, Tags: cur.Tags, Namespace: app.Cfg.Namespace})
			if err != nil {
				return err
			}
			if eResp.OK && eResp.Entry != nil {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\n", eResp.Entry.ID, eResp.Entry.Title)
				return nil
			}
			if eResp.Msg != "conflict" {
				if eResp.Msg != "" {
					return errors.New(eResp.Msg)
				}
				return db.ErrConflict
			}

			// Conflict: load latest and optionally reopen
			latest := cur
			if show2, gerr := ipc.Request(cmd.Context(), sock, ipc.Message{Name: "note.show", ID: id, Namespace: app.Cfg.Namespace}); gerr == nil && show2.Entry != nil {
				latest = *show2.Entry
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "Conflict: note has changed since you opened it.")
			if cur.Title != latest.Title {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "- remote title: %q\n+ local  title: %q\n", latest.Title, cur.Title)
			}
			if cur.Body != latest.Body {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "(body differs)\n")
			}
			if !force {
				return db.ErrConflict
			}

			// Reopen against latest
			reopen := composeEditorContent(latest.Title, latest.Tags, latest.Body)
			out2, changed2, err := openEditorAt(path, []byte(reopen))
			if err != nil {
				return err
			}
			if !keepTmp {
				_ = os.Remove(path)
			}
			if !changed2 {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "No changes.")
				return nil
			}
			t2, tg2, b2 := parseEditedNote(string(out2))
			if t2 == "" && strings.TrimSpace(b2) == "" {
				return fmt.Errorf("edit aborted: empty content")
			}
			if t2 == "" {
				t2 = firstLine(b2)
			}
			latest.Title, latest.Tags, latest.Body = t2, tg2, b2
			latest.UpdatedAt = time.Now().UTC()
			e2, err := ipc.Request(cmd.Context(), sock, ipc.Message{Name: "note.edit", ID: latest.ID, IfVersion: latest.Version, Title: latest.Title, Body: latest.Body, Tags: latest.Tags, Namespace: app.Cfg.Namespace})
			if err != nil {
				return err
			}
			if !e2.OK || e2.Entry == nil {
				if e2.Msg != "" {
					return errors.New(e2.Msg)
				}
				return db.ErrConflict
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\n", e2.Entry.ID, e2.Entry.Title)
			return nil
		},
	}
	cmd.Flags().BoolVar(&keepTmp, "keep-tmp", false, "keep temporary editor file after save")
	cmd.Flags().BoolVar(&force, "force", false, "on conflict, reopen against latest for another edit")
	return cmd
}
