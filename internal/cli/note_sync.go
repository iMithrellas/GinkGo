package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
)

func newNoteSyncCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Replicate local note changes to remotes",
		RunE: func(cmd *cobra.Command, args []string) error {
			app := getApp(cmd)
			if err := app.Syncer.SyncNow(context.Background()); err != nil {
				return err
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "sync: completed push to remotes")
			return nil
		},
	}
	return cmd
}
