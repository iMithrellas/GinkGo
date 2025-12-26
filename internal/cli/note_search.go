package cli

import (
	"fmt"
	"strings"

	"github.com/mithrel/ginkgo/internal/ipc"
	"github.com/mithrel/ginkgo/internal/present"
	"github.com/mithrel/ginkgo/internal/util"
	"github.com/spf13/cobra"
)

func newNoteSearchCmd() *cobra.Command {
	var filters FilterOpts
	var outputMode string
	var noHeaders bool
	cmd := &cobra.Command{
		Use:   "search",
		Short: "Search notes (fts|regex)",
	}

	// Full-text search
	fts := &cobra.Command{
		Use:   "fts <query>",
		Short: "Full-text style search",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			q := args[0]
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

			req := ipc.Message{
				Name:      "note.search.fts",
				Title:     q,
				Namespace: resolveNamespace(cmd),
				TagsAny:   any,
				TagsAll:   all,
				Since:     sinceStr, // RFC3339 or ""
				Until:     untilStr, // RFC3339 or ""
			}

			resp, err := ipc.Request(cmd.Context(), sock, req)
			if err != nil {
				return err
			}
			mode, ok := present.ParseMode(strings.ToLower(outputMode))
			if !ok || mode == present.ModeTUI {
				return fmt.Errorf("invalid --output: %s", outputMode)
			}
			opts := present.Options{Mode: mode, JSONIndent: false, Headers: !noHeaders}
			return present.RenderEntries(cmd.Context(), cmd.OutOrStdout(), resp.Entries, opts)
		},
	}

	// Regex search (narrowed via trigram-like prefilter in daemon)
	rx := &cobra.Command{
		Use:   "regex <pattern>",
		Short: "Regex search (with FTS narrowing)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			pattern := args[0]
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
			req := ipc.Message{
				Name:      "note.search.regex",
				Title:     pattern,
				Namespace: resolveNamespace(cmd),
				TagsAny:   any,
				TagsAll:   all,
				Since:     sinceStr, // RFC3339 or ""
				Until:     untilStr, // RFC3339 or ""
			}

			resp, err := ipc.Request(cmd.Context(), sock, req)
			if err != nil {
				return err
			}
			mode, ok := present.ParseMode(strings.ToLower(outputMode))
			if !ok || mode == present.ModeTUI {
				return fmt.Errorf("invalid --output: %s", outputMode)
			}
			opts := present.Options{Mode: mode, JSONIndent: false, Headers: !noHeaders}
			return present.RenderEntries(cmd.Context(), cmd.OutOrStdout(), resp.Entries, opts)
		},
	}

	cmd.AddCommand(fts, rx)
	addFilterFlags(cmd, &filters)
	cmd.PersistentFlags().StringVar(&outputMode, "output", "plain", "output mode: plain|pretty|json")
	_ = cmd.RegisterFlagCompletionFunc("output", func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		return []string{"plain", "pretty", "json"}, cobra.ShellCompDirectiveNoFileComp
	})
	// Cobra supports only single-letter shorthand; using -H for --noheaders
	cmd.PersistentFlags().BoolVarP(&noHeaders, "noheaders", "H", false, "hide column headers (plain)")
	return cmd
}
