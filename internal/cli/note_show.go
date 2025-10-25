package cli

import (
	"errors"
	"fmt"

	"github.com/mithrel/ginkgo/internal/ipc"
	"github.com/mithrel/ginkgo/internal/ui"
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
			_, _ = fmt.Fprint(cmd.OutOrStdout(), ui.FormatEntry(*resp.Entry))
			return nil
		},
	}
	return cmd
}
