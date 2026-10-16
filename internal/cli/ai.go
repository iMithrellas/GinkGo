package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/mithrel/ginkgo/internal/ipc"
	"github.com/mithrel/ginkgo/pkg/api"
)

const maxAINoteInput = 256 * 1024

func newAICmd() *cobra.Command {
	cmd := &cobra.Command{Use: "ai", Short: "Optional AI-assisted notes"}
	cmd.PersistentFlags().StringP("namespace", "n", "", "override namespace for this command")
	registerNamespaceCompletion(cmd)
	cmd.AddCommand(newAINoteCmd())
	return cmd
}

func newAINoteCmd() *cobra.Command {
	var stdin, forceNew bool
	var provider, target, title, output string
	var tags []string
	var timeout time.Duration
	cmd := &cobra.Command{Use: "note --stdin", Short: "Create or expand a note from original text", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !stdin {
				return errors.New("--stdin is required")
			}
			if forceNew && target != "" {
				return errors.New("--new and --note are mutually exclusive")
			}
			if timeout <= 0 {
				return errors.New("--timeout must be positive")
			}
			if output != "text" && output != "json" {
				return errors.New("--output must be text or json")
			}
			text, err := readAINoteInput(cmd.InOrStdin())
			if err != nil {
				return err
			}
			ns := resolveNamespace(cmd)
			if err := ensureNamespaceConfigured(cmd, ns); err != nil {
				return err
			}
			action := "auto"
			if forceNew {
				action = "create"
			}
			if target != "" {
				action = "expand"
			}
			sock, err := ipc.SocketPath()
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
			defer cancel()
			r, err := ipc.Request(ctx, sock, ipc.Message{Name: "ai.note", Text: text, Namespace: ns, Title: title, Tags: tags, Provider: provider, NoteID: target, Action: action})
			if err != nil {
				return err
			}
			if !r.OK || r.Entry == nil {
				if r.Msg != "" {
					return errors.New(r.Msg)
				}
				return errors.New("AI note failed")
			}
			if output == "json" {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(struct {
					Entry          api.Entry `json:"entry"`
					Action         string    `json:"action"`
					FallbackReason string    `json:"fallback_reason"`
				}{*r.Entry, r.Action, r.FallbackReason})
			}
			if r.FallbackReason != "" {
				fmt.Fprintf(cmd.ErrOrStderr(), "Raw fallback: %s\n", r.FallbackReason)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\n", r.Entry.ID, r.Entry.Title)
			return err
		},
	}
	cmd.Flags().BoolVar(&stdin, "stdin", false, "read exact original input from stdin (max 256 KiB)")
	cmd.Flags().BoolVar(&forceNew, "new", false, "always create a new note")
	cmd.Flags().StringVar(&target, "note", "", "expand this note ID")
	cmd.Flags().StringVar(&provider, "provider", "", "configured provider name")
	cmd.Flags().StringVar(&title, "title", "", "explicit title override")
	cmd.Flags().StringArrayVar(&tags, "tag", nil, "tag override (repeatable)")
	cmd.Flags().StringVar(&output, "output", "text", "output: json|text")
	cmd.Flags().DurationVar(&timeout, "timeout", 3*time.Minute, "request deadline")
	cmd.MarkFlagsMutuallyExclusive("new", "note")
	return cmd
}

func readAINoteInput(r io.Reader) (string, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxAINoteInput+1))
	if err != nil {
		return "", fmt.Errorf("read input: %w", err)
	}
	if len(data) > maxAINoteInput {
		return "", errors.New("input exceeds 256 KiB")
	}
	if strings.TrimSpace(string(data)) == "" {
		return "", errors.New("input is empty")
	}
	return string(data), nil
}
