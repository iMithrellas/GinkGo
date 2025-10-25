package cli

import (
	"fmt"

	"github.com/mithrel/ginkgo/internal/ipc"
	"github.com/spf13/cobra"
)

func newNoteSearchCmd() *cobra.Command {
	var tagsAnyCSV string
	var tagsAllCSV string
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
			sock, err := ipc.SocketPath()
			if err != nil {
				return err
			}
			any := splitCSV(tagsAnyCSV)
			all := splitCSV(tagsAllCSV)
			resp, err := ipc.Request(cmd.Context(), sock, ipc.Message{Name: "note.search.fts", Title: q, Namespace: resolveNamespace(cmd), TagsAny: any, TagsAll: all})
			if err != nil {
				return err
			}
			for _, e := range resp.Entries {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\n", e.ID, e.Title)
			}
			return nil
		},
	}

	// Regex search (narrowed via trigram-like prefilter in daemon)
	rx := &cobra.Command{
		Use:   "regex <pattern>",
		Short: "Regex search (with FTS narrowing)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			pattern := args[0]
			sock, err := ipc.SocketPath()
			if err != nil {
				return err
			}
			any := splitCSV(tagsAnyCSV)
			all := splitCSV(tagsAllCSV)
			resp, err := ipc.Request(cmd.Context(), sock, ipc.Message{Name: "note.search.regex", Title: pattern, Namespace: resolveNamespace(cmd), TagsAny: any, TagsAll: all})
			if err != nil {
				return err
			}
			for _, e := range resp.Entries {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\n", e.ID, e.Title)
			}
			return nil
		},
	}

	cmd.AddCommand(fts, rx)
	cmd.PersistentFlags().StringVar(&tagsAnyCSV, "tags-any", "", "comma-separated tags; match notes containing any")
	cmd.PersistentFlags().StringVar(&tagsAllCSV, "tags-all", "", "comma-separated tags; match notes containing all")
	return cmd
}
