package cli

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/mithrel/ginkgo/internal/ipc"
	"github.com/mithrel/ginkgo/internal/present"
	"github.com/mithrel/ginkgo/internal/util"
	"github.com/spf13/cobra"
)

func newNoteListCmd() *cobra.Command {
	var filters FilterOpts
	var outputMode string
	var noHeaders bool
	var pageSize int
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List notes",
		RunE: func(cmd *cobra.Command, args []string) error {
			app := getApp(cmd)
			sinceStr, untilStr, err := util.NormalizeTimeRange(filters.Since, filters.Until)
			if err != nil {
				return err
			}
			any := splitCSV(filters.TagsAny)
			all := splitCSV(filters.TagsAll)

			sock, err := ipc.SocketPath()
			if err != nil {
				return err
			}

			if pageSize <= 0 {
				pageSize = app.Cfg.GetInt("export.page_size")
			}
			mode, ok := present.ParseMode(strings.ToLower(outputMode))
			if !ok {
				return fmt.Errorf("invalid --output: %s", outputMode)
			}
			dur := time.Duration(0)
			if !ok {
				return fmt.Errorf("invalid --output: %s", outputMode)
			}
			opts := present.Options{
				Mode:            mode,
				JSONIndent:      false, // pretty-print via external tools like jq
				Headers:         !noHeaders,
				InitialStatus:   fmt.Sprintf("loaded successfully"),
				InitialDuration: dur,
				FilterTagsAny:   filters.TagsAny,
				FilterTagsAll:   filters.TagsAll,
				FilterSince:     filters.Since,
				FilterUntil:     filters.Until,
				Namespace:       resolveNamespace(cmd),
				TUIBufferRatio:  app.Cfg.GetFloat64("tui.buffer_ratio"),
			}
			if mode == present.ModeTUI {
				return renderEntries(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), nil, opts)
			}
			return withPager(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), func(w io.Writer) error {
				writer := newEntryStreamWriter(w, opts)
				return streamEntries(cmd.Context(), sock, pageSize, func(cursor string) ipc.Message {
					return ipc.Message{
						Name:      "note.list",
						Namespace: resolveNamespace(cmd),
						TagsAny:   any,
						TagsAll:   all,
						Since:     sinceStr, // RFC3339 string or ""
						Until:     untilStr, // RFC3339 string or ""
					}
				}, writer)
			})
		},
	}
	addFilterFlags(cmd, &filters)
	cmd.Flags().StringVar(&outputMode, "output", "tui", "output mode: plain|pretty|json|tui")
	cmd.Flags().IntVar(&pageSize, "page-size", 0, "page size for export paging (0 uses config)")
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
