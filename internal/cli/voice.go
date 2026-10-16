package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/mithrel/ginkgo/internal/ipc"
	"github.com/mithrel/ginkgo/pkg/api"
)

func newVoiceCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "voice", Short: "Optional local WAV transcription"}
	cmd.PersistentFlags().StringP("namespace", "n", "", "override namespace for saved notes")
	registerNamespaceCompletion(cmd)
	cmd.AddCommand(newVoiceTranscribeCmd())
	return cmd
}

func newVoiceTranscribeCmd() *cobra.Command {
	var save, forceNew bool
	var mode, provider, target, title, model, language, output string
	var tags []string
	var timeout time.Duration
	cmd := &cobra.Command{Use: "transcribe FILE", Short: "Transcribe a local PCM WAV with installed whisper-cli", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !cmd.Flags().Changed("mode") {
				mode = getApp(cmd).Cfg.GetString("voice.mode")
				if mode == "" {
					mode = "raw"
				}
			}
			if mode != "raw" && mode != "ai" {
				return errors.New("--mode must be raw or ai")
			}
			if forceNew && target != "" {
				return errors.New("--new and --note are mutually exclusive")
			}
			if (forceNew || target != "") && (!save || mode != "ai") {
				return errors.New("--new and --note require --save --mode ai")
			}
			if timeout <= 0 {
				return errors.New("--timeout must be positive")
			}
			if output != "text" && output != "json" {
				return errors.New("--output must be text or json")
			}
			path, err := filepath.Abs(args[0])
			if err != nil {
				return err
			}
			modelPath := model
			if modelPath != "" {
				modelPath, err = filepath.Abs(modelPath)
				if err != nil {
					return err
				}
			}
			ns := resolveNamespace(cmd)
			if save {
				if err := ensureNamespaceConfigured(cmd, ns); err != nil {
					return err
				}
			}
			sock, err := ipc.SocketPath()
			if err != nil {
				return err
			}
			action := "auto"
			if forceNew {
				action = "create"
			}
			if target != "" {
				action = "expand"
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
			defer cancel()
			r, err := ipc.Request(ctx, sock, ipc.Message{Name: "voice.transcribe", AudioPath: path, Save: save, Mode: mode, Namespace: ns, Title: title, Tags: tags, Provider: provider, Action: action, NoteID: target, Model: modelPath, Language: language})
			if err != nil {
				return err
			}
			if !r.OK {
				if r.Msg != "" {
					return errors.New(r.Msg)
				}
				return errors.New("voice transcription failed")
			}
			if r.Transcript == nil {
				return errors.New("missing transcript in daemon response")
			}
			if output == "json" {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(struct {
					Transcript     *ipc.Transcript `json:"transcript"`
					Entry          *api.Entry      `json:"entry,omitempty"`
					Action         string          `json:"action,omitempty"`
					FallbackReason string          `json:"fallback_reason,omitempty"`
				}{r.Transcript, r.Entry, r.Action, r.FallbackReason})
			}
			if r.FallbackReason != "" {
				fmt.Fprintf(cmd.ErrOrStderr(), "Raw fallback: %s\n", r.FallbackReason)
			}
			if r.Entry != nil {
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\n", r.Entry.ID, r.Entry.Title)
			} else {
				_, err = fmt.Fprint(cmd.OutOrStdout(), r.Transcript.Text)
			}
			return err
		},
	}
	cmd.Flags().BoolVar(&save, "save", false, "save the transcript as a note")
	cmd.Flags().StringVar(&mode, "mode", "raw", "note mode: raw|ai")
	cmd.Flags().StringVar(&provider, "provider", "", "configured AI provider name")
	cmd.Flags().BoolVar(&forceNew, "new", false, "force a new AI note (requires --save --mode ai)")
	cmd.Flags().StringVar(&target, "note", "", "expand this note ID (requires --save --mode ai)")
	cmd.Flags().StringVar(&title, "title", "", "explicit saved title")
	cmd.Flags().StringArrayVar(&tags, "tag", nil, "tag override (repeatable)")
	cmd.Flags().StringVar(&model, "model", "", "installed whisper.cpp GGML model path")
	cmd.Flags().StringVar(&language, "language", "", "STT language code or auto")
	cmd.Flags().DurationVar(&timeout, "timeout", 8*time.Minute, "whole request deadline")
	cmd.Flags().StringVar(&output, "output", "text", "output: text|json")
	cmd.MarkFlagsMutuallyExclusive("new", "note")
	return cmd
}
