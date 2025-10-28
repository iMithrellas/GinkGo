package cli

import (
	"errors"
	"fmt"

	"github.com/mithrel/ginkgo/internal/ipc"
	"github.com/spf13/cobra"
)

func newNoteDeleteCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete <id>",
		Short: "Delete a note",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]
			sock, err := ipc.SocketPath()
			if err != nil {
				return err
			}
			resp, err := ipc.Request(cmd.Context(), sock, ipc.Message{Name: "note.delete", ID: id, Namespace: resolveNamespace(cmd)})
			if err != nil {
				return err
			}
			if !resp.OK {
				if resp.Msg != "" {
					return errors.New(resp.Msg)
				}
				return errors.New("not found")
			}
			fmt.Printf("Note ID %s deleted successfully.\n", id)
			return nil
		},
	}
	return cmd
}
