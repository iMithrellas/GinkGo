package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newNoteSyncCmd() *cobra.Command {
	var background bool
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Replicate local note changes to remotes",
		RunE: func(cmd *cobra.Command, args []string) error {
			app := getApp(cmd)
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "[dry-run] note sync background=%v\n", background)
			_ = app
			return nil
		},
	}
	cmd.Flags().BoolVar(&background, "daemon", false, "run continuous background sync")
	return cmd
}
