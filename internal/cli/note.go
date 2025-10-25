package cli

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/mithrel/ginkgo/internal/ipc"
	"github.com/spf13/cobra"
)

// newNoteCmd defines the parent "note" command.
// Running "ginkgo-cli note" without subcommands adds a note.
func newNoteCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "note [title]",
		Short: "Work with notes (default: add one-liner)",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			app := getApp(cmd)
			if len(args) > 0 {
				// One-liner: Title only → daemon handles creation
				title := strings.TrimSpace(strings.Join(args, " "))
				if title == "" {
					return fmt.Errorf("empty title")
				}
				sock, err := ipc.SocketPath()
				if err != nil {
					return err
				}
				resp, err := ipc.Request(cmd.Context(), sock, ipc.Message{Name: "note.add", Title: title, Tags: app.Cfg.DefaultTags, Namespace: app.Cfg.Namespace})
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

			// Editor flow: ask daemon to create an empty note, then update
			sock, err := ipc.SocketPath()
			if err != nil {
				return err
			}
			resp, err := ipc.Request(cmd.Context(), sock, ipc.Message{Name: "note.add", Namespace: app.Cfg.Namespace})
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
			path, err := editorPathForID(e.ID)
			if err != nil {
				return err
			}
			initial := []byte(composeEditorContent(e.Title, e.Tags, e.Body))
			out, changed, err := openEditorAt(path, initial)
			if err != nil {
				return err
			}
			_ = os.Remove(path)
			if !changed {
				if app.Cfg.Editor.DeleteEmpty {
					_, _ = ipc.Request(cmd.Context(), sock, ipc.Message{Name: "note.delete", ID: e.ID, Namespace: app.Cfg.Namespace})
				}
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "No edits; note unchanged.")
				return nil
			}
			title, tags, body := parseEditedNote(string(out))
			if title == "" && strings.TrimSpace(body) == "" {
				if app.Cfg.Editor.DeleteEmpty {
					_, _ = ipc.Request(cmd.Context(), sock, ipc.Message{Name: "note.delete", ID: e.ID, Namespace: app.Cfg.Namespace})
				}
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "Note aborted: empty content.")
				return nil
			}
			if title == "" {
				title = firstLine(body)
			}
			// Apply default tags if none provided
			if len(tags) == 0 {
				tags = append(tags, app.Cfg.DefaultTags...)
			}
			if title == "" {
				if app.Cfg.Editor.DeleteEmpty {
					_, _ = ipc.Request(cmd.Context(), sock, ipc.Message{Name: "note.delete", ID: e.ID, Namespace: app.Cfg.Namespace})
				}
				return fmt.Errorf("note aborted: empty content")
			}
			resp2, err := ipc.Request(cmd.Context(), sock, ipc.Message{Name: "note.edit", ID: e.ID, IfVersion: e.Version, Title: title, Body: body, Tags: tags, Namespace: app.Cfg.Namespace})
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
		},
	}

	// Attach subcommands under note
	cmd.AddCommand(newNoteEditCmd())
	cmd.AddCommand(newNoteShowCmd())
	cmd.AddCommand(newNoteListCmd())
	cmd.AddCommand(newNoteSearchCmd())
	cmd.AddCommand(newNoteSyncCmd())

	return cmd
}
