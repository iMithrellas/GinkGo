package cli

import (
	"github.com/spf13/cobra"
	"strings"
)

// newNoteCmd defines the parent "note" command.
// Running "ginkgo-cli note" without subcommands adds a note.
func newNoteCmd() *cobra.Command {
	var tagsFlag []string
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

	// Flags: allow tags for one-liner adds
	cmd.Flags().StringSliceVarP(&tagsFlag, "tags", "t", nil, "tags for one-liner add (comma-separated or repeated)")
	cmd.Flags().StringSliceVarP(&tagsFlag, "namespace", "n", nil, "override namespace for this command")

	return cmd
}

// resolveNamespace checks for a --namespace flag; if not set, uses app config.
func resolveNamespace(cmd *cobra.Command) string {
	app := getApp(cmd)
	ns, _ := cmd.Flags().GetString("namespace")
	if strings.TrimSpace(ns) != "" {
		return ns
	}
	return app.Cfg.Namespace
}
