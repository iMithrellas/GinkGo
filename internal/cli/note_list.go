package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/mithrel/ginkgo/internal/ipc"
	"github.com/mithrel/ginkgo/internal/present"
	"github.com/spf13/cobra"
)

func newNoteListCmd() *cobra.Command {
	var filters FilterOpts
	var outputMode string
	var noHeaders bool
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

			start := time.Now()
			resp, err := ipc.Request(cmd.Context(), sock, req)
			dur := time.Since(start)
			if err != nil {
				return err
			}
			mode, ok := present.ParseMode(strings.ToLower(outputMode))
			if !ok {
				return fmt.Errorf("invalid --output: %s", outputMode)
			}
			opts := present.Options{
				Mode:            mode,
				JSONIndent:      false, // pretty-print via external tools like jq
				Headers:         !noHeaders,
				InitialStatus:   fmt.Sprintf("loaded successfully"),
				InitialDuration: dur,
			}
			return present.RenderEntries(cmd.Context(), cmd.OutOrStdout(), resp.Entries, opts)
		},
	}
	addFilterFlags(cmd, &filters)
	cmd.Flags().StringVar(&outputMode, "output", "plain", "output mode: plain|pretty|json|tui")
	_ = cmd.RegisterFlagCompletionFunc("output", func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		return []string{"plain", "pretty", "json", "tui"}, cobra.ShellCompDirectiveNoFileComp
	})
	cmd.Flags().BoolVar(&noHeaders, "noheaders", false, "hide column headers (plain/tui)")
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
