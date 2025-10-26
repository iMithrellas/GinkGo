package cli

import (
	"fmt"
	"strings"

	"github.com/mithrel/ginkgo/internal/ipc"
	"github.com/mithrel/ginkgo/internal/ui"
	"github.com/spf13/cobra"
)

func newNoteListCmd() *cobra.Command {
	var filters FilterOpts
	var useBubble bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List notes",
		RunE: func(cmd *cobra.Command, args []string) error {
			sinceStr, untilStr, err := normalizeTimeRange(filters.Since, filters.Until)
			if err != nil {
				return err
			}
			any := splitCSV(filters.TagsAny)
			all := splitCSV(filters.TagsAll)

			sock, err := ipc.SocketPath()
			if err != nil {
				return err
			}

			req := ipc.Message{
				Name:      "note.list",
				Namespace: resolveNamespace(cmd),
				TagsAny:   any,
				TagsAll:   all,
				Since:     sinceStr, // RFC3339 string or ""
				Until:     untilStr, // RFC3339 string or ""
			}

			resp, err := ipc.Request(cmd.Context(), sock, req)
			if err != nil {
				return err
			}
			if useBubble {
				if err := ui.RenderEntriesTable(cmd.Context(), resp.Entries); err != nil {
					return err
				}
				return nil
			}
			for _, e := range resp.Entries {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\n", e.ID, e.Title)
			}
			return nil
		},
	}
	addFilterFlags(cmd, &filters)
	cmd.Flags().BoolVar(&useBubble, "bubble", false, "render interactive table")
	return cmd
}

// splitCSV splits a comma-separated list into trimmed non-empty strings.
func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		t := strings.TrimSpace(p)
		if t != "" {
			out = append(out, t)
		}
	}
	return out
}
