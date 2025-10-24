package cli

import (
    "fmt"

    "github.com/spf13/cobra"
)

func newEditCmd() *cobra.Command {
    cmd := &cobra.Command{
        Use:   "edit <id>",
        Short: "Edit an existing entry",
        Args:  cobra.ExactArgs(1),
        RunE: func(cmd *cobra.Command, args []string) error {
            _ = getApp(cmd)
            id := args[0]
            _, _ = fmt.Fprintf(cmd.OutOrStdout(), "[dry-run] edit id=%s (launch $EDITOR)\n", id)
            return nil
        },
    }
    return cmd
}

