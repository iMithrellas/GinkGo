package cli

import (
	"fmt"
	"strings"

	"github.com/mithrel/ginkgo/internal/ipc"
	"github.com/spf13/cobra"
)

func newNoteListCmd() *cobra.Command {
	var tagsAnyCSV string
	var tagsAllCSV string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List notes",
		RunE: func(cmd *cobra.Command, args []string) error {
			app := getApp(cmd)
			sock, err := ipc.SocketPath()
			if err != nil {
				return err
			}
			any := splitCSV(tagsAnyCSV)
			all := splitCSV(tagsAllCSV)
			resp, err := ipc.Request(cmd.Context(), sock, ipc.Message{Name: "note.list", Namespace: app.Cfg.Namespace, TagsAny: any, TagsAll: all})
			if err != nil {
				return err
			}
			for _, e := range resp.Entries {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\n", e.ID, e.Title)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&tagsAnyCSV, "tags-any", "", "comma-separated tags; match notes containing any")
	cmd.Flags().StringVar(&tagsAllCSV, "tags-all", "", "comma-separated tags; match notes containing all")
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
