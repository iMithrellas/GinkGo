package cli

import (
	"fmt"
	"strings"

	"github.com/mithrel/ginkgo/internal/ipc"
	"github.com/mithrel/ginkgo/internal/ui"
	"github.com/spf13/cobra"
)

func newNoteListCmd() *cobra.Command {
	var tagsAnyCSV string
	var tagsAllCSV string
	var useBubble bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List notes",
		RunE: func(cmd *cobra.Command, args []string) error {
			sock, err := ipc.SocketPath()
			if err != nil {
				return err
			}
			any := splitCSV(tagsAnyCSV)
			all := splitCSV(tagsAllCSV)
			resp, err := ipc.Request(cmd.Context(), sock, ipc.Message{Name: "note.list", Namespace: resolveNamespace(cmd), TagsAny: any, TagsAll: all})
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
	cmd.Flags().StringVar(&tagsAnyCSV, "tags-any", "", "comma-separated tags; match notes containing any")
	cmd.Flags().StringVar(&tagsAllCSV, "tags-all", "", "comma-separated tags; match notes containing all")
	cmd.Flags().BoolVar(&useBubble, "bubble", false, "render interactive table (requires build with -tags bubble)")
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
