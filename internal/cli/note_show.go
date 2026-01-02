package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/mithrel/ginkgo/internal/ipc"
	"github.com/mithrel/ginkgo/internal/present"
	"github.com/spf13/cobra"
)

func newNoteShowCmd() *cobra.Command {
	var outputMode string
	var headers bool
	cmd := &cobra.Command{
		Use:   "show <id>",
		Short: "Display a note",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]
			sock, err := ipc.SocketPath()
			if err != nil {
				return err
			}
			resp, err := ipc.Request(cmd.Context(), sock, ipc.Message{Name: "note.show", ID: id, Namespace: resolveNamespace(cmd)})
			if err != nil {
				return err
			}
			if !resp.OK || resp.Entry == nil {
				if resp.Msg != "" {
					return errors.New(resp.Msg)
				}
				return errors.New("not found")
			}

			mode, ok := present.ParseMode(strings.ToLower(outputMode))
			if !ok || mode == present.ModeTUI { // tui not applicable
				return fmt.Errorf("invalid --output: %s", outputMode)
			}
			opts := present.Options{Mode: mode, JSONIndent: false, Headers: headers}
			return present.RenderEntry(cmd.Context(), cmd.OutOrStdout(), *resp.Entry, opts)
		},
	}
	cmd.Flags().StringVar(&outputMode, "output", "pretty", "output mode: plain|pretty|json|ndjson")
	_ = cmd.RegisterFlagCompletionFunc("output", func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		return []string{"plain", "pretty", "json", "ndjson"}, cobra.ShellCompDirectiveNoFileComp
	})
	cmd.Flags().BoolVar(&headers, "headers", false, "print header row in plain mode")
	return cmd
}
