package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

func newNoteSyncCmd() *cobra.Command {
	var background bool
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Replicate local note changes to remotes",
		RunE: func(cmd *cobra.Command, args []string) error {
			app := getApp(cmd)
			if background {
				ctx := cmd.Context()
				go app.Syncer.RunBackground(ctx)
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "sync: background worker started")
				for {
					select {
					case <-ctx.Done():
						return ctx.Err()
					default:
						time.Sleep(5 * time.Second)
					}
				}
			}
			if err := app.Syncer.SyncNow(context.Background()); err != nil {
				return err
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "sync: completed push to remotes")
			return nil
		},
	}
	cmd.Flags().BoolVar(&background, "daemon", false, "run continuous background sync")
	return cmd
}
