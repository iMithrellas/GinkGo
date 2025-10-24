package cli

import (
    "fmt"

    "github.com/spf13/cobra"
)

func newSearchCmd() *cobra.Command {
    var in string
    var before string
    var after string

    cmd := &cobra.Command{
        Use:   "search <regex>",
        Short: "Search entries with regex",
        Args:  cobra.ExactArgs(1),
        RunE: func(cmd *cobra.Command, args []string) error {
            _ = getApp(cmd)
            regex := args[0]
            _, _ = fmt.Fprintf(cmd.OutOrStdout(), "[dry-run] search: regex=%q in=%s after=%s before=%s\n", regex, in, after, before)
            return nil
        },
    }
    cmd.Flags().StringVar(&in, "in", "all", "fields to search: body|title|tags|all")
    cmd.Flags().StringVar(&before, "before", "", "limit results before date (RFC3339)")
    cmd.Flags().StringVar(&after, "after", "", "limit results after date (RFC3339)")
    return cmd
}

