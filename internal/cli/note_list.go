package cli

import (
	"fmt"

	"github.com/mithrel/ginkgo/internal/ipc"
	"github.com/spf13/cobra"
)

func newNoteListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List notes",
		RunE: func(cmd *cobra.Command, args []string) error {
			app := getApp(cmd)
			sock, err := ipc.SocketPath()
			if err != nil {
				return err
			}
			resp, err := ipc.Request(cmd.Context(), sock, ipc.Message{Name: "note.list", Namespace: app.Cfg.Namespace})
			if err != nil {
				return err
			}
			for _, e := range resp.Entries {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\n", e.ID, e.Title)
			}
			return nil
		},
	}
	return cmd
}
