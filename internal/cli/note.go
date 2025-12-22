package cli

import (
	"strings"

	"github.com/mithrel/ginkgo/internal/ipc"
	"github.com/spf13/cobra"
)

// FilterOpts holds common filtering options for note commands.
type FilterOpts struct {
	TagsAny string
	TagsAll string
	Since   string
	Until   string
}

// newNoteCmd defines the parent "note" command.
// Running "ginkgo-cli note" without subcommands adds a note.
func newNoteCmd() *cobra.Command {
	var tagsFlag []string
	var nsFlag string
	cmd := &cobra.Command{
		Use:   "note [title]",
		Short: "Work with notes (default: add one-liner)",
		Args:  cobra.ArbitraryArgs,
		RunE:  runNoteAdd,
	}

	// Attach subcommands under note
	cmd.AddCommand(newNoteAddCmd())
	cmd.AddCommand(newNoteEditCmd())
	cmd.AddCommand(newNoteShowCmd())
	cmd.AddCommand(newNoteDeleteCmd())
	cmd.AddCommand(newNoteListCmd())
	cmd.AddCommand(newNoteSearchCmd())
	cmd.AddCommand(newNoteSyncCmd())
	cmd.AddCommand(newNoteQueueCmd())

	// Flags: allow tags for one-liner adds and an optional namespace override
	cmd.Flags().StringSliceVarP(&tagsFlag, "tags", "t", nil, "tags for one-liner add (comma-separated or repeated)")
	cmd.PersistentFlags().StringVarP(&nsFlag, "namespace", "n", "", "override namespace for this command")

	_ = cmd.RegisterFlagCompletionFunc("namespace", func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		sock, err := ipc.SocketPath()
		if err != nil {
			return nil, cobra.ShellCompDirectiveError
		}
		resp, err := ipc.Request(cmd.Context(), sock, ipc.Message{Name: "namespace.list"})
		if err != nil || !resp.OK {
			return nil, cobra.ShellCompDirectiveError
		}
		return resp.Namespaces, cobra.ShellCompDirectiveNoFileComp
	})

	return cmd
}

// resolveNamespace checks for a --namespace flag; if not set, uses app config.
func resolveNamespace(cmd *cobra.Command) string {
	ns, _ := cmd.Flags().GetString("namespace")
	if strings.TrimSpace(ns) != "" {
		return ns
	}
	app := getApp(cmd)
	return app.Cfg.GetString("namespace")
}

// addFilterFlags adds common filtering flags to a command.
func addFilterFlags(cmd *cobra.Command, opts *FilterOpts) {
	cmd.PersistentFlags().StringVar(&opts.TagsAny, "tags-any", "", "comma-separated tags; match notes containing any")
	cmd.PersistentFlags().StringVar(&opts.TagsAll, "tags-all", "", "comma-separated tags; match notes containing all")
	cmd.PersistentFlags().StringVarP(&opts.Since, "since", "s", "", "Show notes created since a time (absolute: '2025-10-26T14:30', relative: '2h', '3d')")
	cmd.PersistentFlags().StringVarP(&opts.Until, "until", "u", "", "Show notes created until a time (absolute or relative; same formats as --since)")
}
