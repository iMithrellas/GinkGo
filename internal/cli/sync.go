package cli

import (
    "fmt"

    "github.com/spf13/cobra"
)

func newSyncCmd() *cobra.Command {
    var background bool
    cmd := &cobra.Command{
        Use:   "sync",
        Short: "Replicate local changes to remotes",
        RunE: func(cmd *cobra.Command, args []string) error {
            app := getApp(cmd)
            _, _ = fmt.Fprintf(cmd.OutOrStdout(), "[dry-run] sync background=%v\n", background)
            _ = app
            return nil
        },
    }
    cmd.Flags().BoolVar(&background, "daemon", false, "run continuous background sync")
    return cmd
}

