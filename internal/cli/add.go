package cli

import (
    "fmt"
    "time"

    "github.com/spf13/cobra"

    "github.com/mithrel/ginkgo/pkg/api"
)

func newAddCmd() *cobra.Command {
    var tags []string
    var title string
    var body string

    cmd := &cobra.Command{
        Use:   "add",
        Short: "Add a new journal entry",
        RunE: func(cmd *cobra.Command, args []string) error {
            app := getApp(cmd)
            entry := api.Entry{
                Title:     title,
                Body:      body,
                Tags:      tags,
                CreatedAt: time.Now().UTC(),
            }
            // Wireframe: just print; real impl writes to event log.
            _, _ = fmt.Fprintf(cmd.OutOrStdout(), "[dry-run] add: %+v\n", entry)
            _ = app // placeholder usage
            return nil
        },
    }

    cmd.Flags().StringVarP(&title, "title", "t", "", "entry title")
    cmd.Flags().StringSliceVarP(&tags, "tag", "g", nil, "tags for the entry")
    cmd.Flags().StringVarP(&body, "body", "b", "", "entry body (use editor if empty)")
    return cmd
}

