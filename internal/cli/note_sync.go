package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/mithrel/ginkgo/internal/ipc"
)

func newNoteSyncCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Replicate local note changes to remotes",
		RunE: func(cmd *cobra.Command, args []string) error {
			sock, err := ipc.SocketPath()
			if err != nil {
				return err
			}
			resp, err := ipc.Request(context.Background(), sock, ipc.Message{Name: "sync.run"})
			if err != nil {
				return err
			}
			if !resp.OK {
				return errors.New(resp.Msg)
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "sync: triggered")
			return nil
		},
	}
	return cmd
}
