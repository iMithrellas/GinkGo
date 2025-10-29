package cli

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mithrel/ginkgo/internal/editor"
	"github.com/mithrel/ginkgo/internal/ipc"
)

// newNoteAddCmd registers `note add`, but doesn't own wiring; parent calls it.
func newNoteAddCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "add [title]",
		Short: "Add a new note",
		Args:  cobra.ArbitraryArgs,
		RunE:  runNoteAdd, // shared with parent
	}
	// Tags only apply to one-liner usage of add
	cmd.Flags().StringSliceP("tags", "t", nil, "tags for one-liner add (comma-separated or repeated)")
	return cmd
}

// runNoteAdd is the default behavior used by both parent RunE and `note add`.
func runNoteAdd(cmd *cobra.Command, args []string) error {
	app := getApp(cmd)

	sock, err := ipc.SocketPath()
	if err != nil {
		return err
	}

	// One-liner flow
	if len(args) > 0 {
		title := strings.TrimSpace(strings.Join(args, " "))
		if title == "" {
			return fmt.Errorf("empty title")
		}
		tags, _ := cmd.Flags().GetStringSlice("tags")
		if len(tags) == 0 {
			tags = app.Cfg.GetStringSlice("default_tags")
		}
		resp, err := ipc.Request(cmd.Context(), sock, ipc.Message{
			Name:      "note.add",
			Title:     title,
			Tags:      tags,
			Namespace: resolveNamespace(cmd),
		})
		if err != nil {
			return err
		}
		if !resp.OK || resp.Entry == nil {
			if resp.Msg != "" {
				return errors.New(resp.Msg)
			}
			return errors.New("failed to add note")
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\n", resp.Entry.ID, resp.Entry.Title)
		return nil
	}

	// Editor flow
	resp, err := ipc.Request(cmd.Context(), sock, ipc.Message{
		Name:      "note.add",
		Namespace: resolveNamespace(cmd),
	})
	if err != nil {
		return err
	}
	if !resp.OK || resp.Entry == nil {
		if resp.Msg != "" {
			return errors.New(resp.Msg)
		}
		return errors.New("failed to create note")
	}
	e := resp.Entry

	path, err := editor.PathForID(e.ID)
	if err != nil {
		return err
	}
	initial := []byte(editor.ComposeContent(e.Title, e.Tags, e.Body))
	out, changed, err := editor.OpenAt(path, initial)
	if err != nil {
		return err
	}
	_ = os.Remove(path)

	if !changed {
		if app.Cfg.GetBool("editor.delete_empty") {
			_, _ = ipc.Request(cmd.Context(), sock, ipc.Message{
				Name:      "note.delete",
				ID:        e.ID,
				Namespace: resolveNamespace(cmd),
			})
		}
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), "No edits; note unchanged.")
		return nil
	}

	title, tags, body := editor.ParseEditedNote(string(out))
	if title == "" && strings.TrimSpace(body) == "" {
		if app.Cfg.GetBool("editor.delete_empty") {
			_, _ = ipc.Request(cmd.Context(), sock, ipc.Message{
				Name:      "note.delete",
				ID:        e.ID,
				Namespace: resolveNamespace(cmd),
			})
		}
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), "Note aborted: empty content.")
		return nil
	}
	if title == "" {
		title = editor.FirstLine(body)
	}
	// Apply default tags if none provided
	if len(tags) == 0 {
		tags = append(tags, app.Cfg.GetStringSlice("default_tags")...)
	}
	if title == "" {
		if app.Cfg.GetBool("editor.delete_empty") {
			_, _ = ipc.Request(cmd.Context(), sock, ipc.Message{
				Name:      "note.delete",
				ID:        e.ID,
				Namespace: resolveNamespace(cmd),
			})
		}
		return fmt.Errorf("note aborted: empty content")
	}

	resp2, err := ipc.Request(cmd.Context(), sock, ipc.Message{
		Name:      "note.edit",
		ID:        e.ID,
		IfVersion: e.Version,
		Title:     title,
		Body:      body,
		Tags:      tags,
		Namespace: resolveNamespace(cmd),
	})
	if err != nil {
		return err
	}
	if !resp2.OK || resp2.Entry == nil {
		if resp2.Msg != "" {
			return errors.New(resp2.Msg)
		}
		return errors.New("failed to save note")
	}

	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\n", resp2.Entry.ID, resp2.Entry.Title)
	return nil
}
