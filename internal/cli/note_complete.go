package cli

import (
	"fmt"

	"github.com/mithrel/ginkgo/internal/ipc"
	"github.com/mithrel/ginkgo/internal/util"
	"github.com/spf13/cobra"
)

func newNoteCompleteTagsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "complete-tags [input]",
		Short: "Get fuzzy matches for tags",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			input := ""
			if len(args) > 0 {
				input = args[0]
			}

			sock, err := ipc.SocketPath()
			if err != nil {
				return err
			}

			ns := resolveNamespace(cmd)
			resp, err := ipc.Request(cmd.Context(), sock, ipc.Message{
				Name:      "tag.list",
				Namespace: ns,
			})
			if err != nil {
				return err
			}
			if !resp.OK {
				return fmt.Errorf("daemon error: %s", resp.Msg)
			}

			// Extract tag strings for scoring
			tags := make([]string, len(resp.Tags))
			for i, t := range resp.Tags {
				tags[i] = t.Tag
			}

			matches := util.ScoreCompletions(input, tags, 20)

			for _, m := range matches {
				fmt.Println(m)
			}

			return nil
		},
	}
}
