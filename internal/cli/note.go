package cli

import (
	"github.com/spf13/cobra"
	"strings"
)

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
	cmd.AddCommand(newNoteListCmd())
	cmd.AddCommand(newNoteSearchCmd())
	cmd.AddCommand(newNoteSyncCmd())

	// Flags: allow tags for one-liner adds and an optional namespace override
	cmd.Flags().StringSliceVarP(&tagsFlag, "tags", "t", nil, "tags for one-liner add (comma-separated or repeated)")
	cmd.PersistentFlags().StringVarP(&nsFlag, "namespace", "n", "", "override namespace for this command")

	return cmd
}

// resolveNamespace checks for a --namespace flag; if not set, uses app config.
func resolveNamespace(cmd *cobra.Command) string {
	ns, _ := cmd.Flags().GetString("namespace")
	if strings.TrimSpace(ns) != "" {
		return ns
	}
	app := getApp(cmd)
	return app.Cfg.Namespace
}
