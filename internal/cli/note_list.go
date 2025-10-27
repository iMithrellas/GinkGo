package cli

import (
	"fmt"
	"strings"

	"github.com/mithrel/ginkgo/internal/ipc"
	"github.com/mithrel/ginkgo/internal/present"
	"github.com/spf13/cobra"
)

func newNoteListCmd() *cobra.Command {
	var filters FilterOpts
	var outputMode string
	var headers bool
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
			mode, ok := present.ParseMode(strings.ToLower(outputMode))
			if !ok {
				return fmt.Errorf("invalid --output: %s", outputMode)
			}
			opts := present.Options{Mode: mode, JSONIndent: outputMode == "json+indent", Headers: headers}
			return present.RenderEntries(cmd.Context(), cmd.OutOrStdout(), resp.Entries, opts)
		},
	}
	addFilterFlags(cmd, &filters)
	cmd.Flags().StringVar(&outputMode, "output", "plain", "output mode: plain|pretty|json|json+indent|tui")
	cmd.Flags().BoolVar(&headers, "headers", false, "print header row in plain mode")
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
