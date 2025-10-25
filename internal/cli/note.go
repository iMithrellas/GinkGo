package cli

import "github.com/spf13/cobra"

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
