package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/mithrel/ginkgo/internal/ipc"
)

func newAIConverseCmd() *cobra.Command {
	var session, provider, output string
	var timeout time.Duration
	cmd := &cobra.Command{Use: "converse [REQUEST]", Short: "Search, read, summarize, create or append through a conversation", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if timeout <= 0 || len(session) > 240 || (output != "text" && output != "json") {
			return errors.New("invalid conversation options")
		}
		var text string
		var err error
		if len(args) == 1 {
			text = args[0]
		} else {
			text, err = readAINoteInput(cmd.InOrStdin())
		}
		if err != nil {
			return err
		}
		ns := resolveNamespace(cmd)
		if err := ensureNamespaceConfigured(cmd, ns); err != nil {
			return err
		}
		sock, err := ipc.SocketPath()
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
		defer cancel()
		response, err := ipc.Request(ctx, sock, ipc.Message{Name: "ai.converse", ID: session, Namespace: ns, Text: text, Provider: provider})
		if err != nil {
			return err
		}
		if !response.OK {
			return errors.New(response.Msg)
		}
		if output == "json" {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(response)
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), response.Msg)
		return err
	}}
	cmd.Flags().StringVar(&session, "session", "default", "conversation session (isolated per namespace; expires after 30 minutes)")
	cmd.Flags().StringVar(&provider, "provider", "", "configured AI provider name")
	cmd.Flags().StringVar(&output, "output", "text", "output: text|json")
	cmd.Flags().DurationVar(&timeout, "timeout", 5*time.Minute, "request deadline")
	return cmd
}
