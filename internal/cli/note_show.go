package cli

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mithrel/ginkgo/internal/ipc"
	"github.com/mithrel/ginkgo/internal/render"
	"github.com/spf13/cobra"
)

func newNoteShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <id>",
		Short: "Display a note",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]
			sock, err := ipc.SocketPath()
			if err != nil {
				return err
			}
			resp, err := ipc.Request(cmd.Context(), sock, ipc.Message{Name: "note.show", ID: id, Namespace: resolveNamespace(cmd)})
			if err != nil {
				return err
			}
			if !resp.OK || resp.Entry == nil {
				if resp.Msg != "" {
					return errors.New(resp.Msg)
				}
				return errors.New("not found")
			}
			e := resp.Entry
			tags := ""
			if len(e.Tags) > 0 {
				tags = strings.Join(e.Tags, ", ")
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "ID: %s\nCreated: %s\nTitle: %s\nTags: %s\n---\n%s\n",
				e.ID, e.CreatedAt.Local().Format(time.RFC3339), e.Title, tags, render.Markdown(e.Body))
			return nil
		},
	}
	return cmd
}
