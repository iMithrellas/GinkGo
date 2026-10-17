package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/mithrel/ginkgo/internal/ipc"
	"github.com/mithrel/ginkgo/pkg/api"
)

func newVoiceCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "voice", Short: "Control live voice capture and transcribe local audio"}
	cmd.PersistentFlags().StringP("namespace", "n", "", "override namespace for saved notes")
	registerNamespaceCompletion(cmd)
	cmd.AddCommand(newVoiceTranscribeCmd())
	cmd.AddCommand(newVoiceWatchCmd())
	cmd.AddCommand(newVoiceStreamCmd())
	cmd.AddCommand(newVoiceControlCmd("enable"), newVoiceControlCmd("disable"), newVoiceControlCmd("listen"), newVoiceControlCmd("status"), newVoiceControlCmd("stop"), newVoiceControlCmd("cancel"))
	return cmd
}

func newVoiceControlCmd(action string) *cobra.Command {
	var silence, maxDuration, startTimeout, timeout time.Duration
	var threshold float64
	var device, mode, provider, title, model, language, output string
	var save, forceNew bool
	var note string
	var tags []string
	aliases := []string(nil)
	if action == "listen" {
		aliases = []string{"record"}
	}
	short := map[string]string{"enable": "Start continuous voice capture", "disable": "Disable continuous capture and discard pending speech", "listen": "Capture one utterance until silence", "status": "Show voice capture status", "stop": "Finalize and stop current capture", "cancel": "Discard current capture"}[action]
	cmd := &cobra.Command{Use: action, Aliases: aliases, Short: short, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		if (action == "listen" || action == "enable") && cmd.Flags().Changed("max-duration") && (maxDuration < 150*time.Millisecond || maxDuration > 5*time.Minute) {
			return errors.New("--max-duration must be between 150ms and 5m")
		}
		if (action == "listen" || action == "enable") && cmd.Flags().Changed("silence") && (silence <= 0 || silence > time.Minute) {
			return errors.New("--silence must be positive and at most 1m")
		}
		if action == "listen" && cmd.Flags().Changed("start-timeout") && (startTimeout <= 0 || startTimeout > 5*time.Minute) {
			return errors.New("--start-timeout must be positive and at most 5m")
		}
		if (action == "listen" || action == "enable") && cmd.Flags().Changed("threshold") && (math.IsNaN(threshold) || math.IsInf(threshold, 0) || threshold <= 0 || threshold > 1) {
			return errors.New("--threshold must be greater than 0 and at most 1")
		}
		if timeout <= 0 {
			return errors.New("--timeout must be positive")
		}
		if (action == "listen" || action == "enable") && !cmd.Flags().Changed("mode") {
			mode = getApp(cmd).Cfg.GetString("voice.mode")
			if mode == "" {
				mode = "raw"
			}
		}
		if (action == "listen" || action == "enable") && mode != "raw" && mode != "ai" {
			return errors.New("--mode must be raw or ai")
		}
		if forceNew && note != "" {
			return errors.New("--new and --note are mutually exclusive")
		}
		if action == "enable" {
			save = true
		}
		if (forceNew || note != "") && (!save || mode != "ai") {
			return errors.New("--new and --note require AI mode and saving (--save for listen)")
		}
		if output != "text" && output != "json" {
			return errors.New("--output must be text or json")
		}
		ns := resolveNamespace(cmd)
		if save && (action == "listen" || action == "enable") {
			if err := ensureNamespaceConfigured(cmd, ns); err != nil {
				return err
			}
		}
		name := "voice." + action
		var capture *ipc.CaptureOptions
		if action == "listen" || action == "enable" {
			capture = &ipc.CaptureOptions{Target: device}
		}
		if capture != nil && cmd.Flags().Changed("silence") {
			capture.SilenceTimeout = silence
		}
		if capture != nil && cmd.Flags().Changed("max-duration") {
			capture.MaxDuration = maxDuration
		}
		if action == "listen" && capture != nil && cmd.Flags().Changed("start-timeout") {
			capture.StartTimeout = startTimeout
		}
		if capture != nil && cmd.Flags().Changed("threshold") {
			capture.Threshold = threshold
		}
		modelPath := model
		if modelPath != "" {
			var pathErr error
			modelPath, pathErr = filepath.Abs(modelPath)
			if pathErr != nil {
				return pathErr
			}
		}
		m := ipc.Message{Name: name, Namespace: ns, Capture: capture, Save: save, Mode: mode, Provider: provider, Title: title, Tags: tags, NoteID: note, Model: modelPath, Language: language, Action: "auto"}
		if forceNew {
			m.Action = "create"
		}
		if note != "" {
			m.Action = "expand"
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
		defer cancel()
		sock, err := ipc.SocketPath()
		if err != nil {
			return err
		}
		r, err := ipc.Request(ctx, sock, m)
		if err != nil {
			return err
		}
		if !r.OK {
			if r.Msg != "" {
				return errors.New(r.Msg)
			}
			return fmt.Errorf("voice %s failed", action)
		}
		if action != "listen" && r.VoiceStatus == nil {
			return errors.New("missing voice status in daemon response")
		}
		if action == "listen" && r.Entry == nil && r.Transcript == nil {
			return errors.New("missing transcript or saved entry in daemon response")
		}
		if output == "json" {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(r)
		}
		if action == "listen" {
			if r.FallbackReason != "" {
				fmt.Fprintf(cmd.ErrOrStderr(), "Raw fallback: %s\n", r.FallbackReason)
			}
			if r.Entry != nil {
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\n", r.Entry.ID, r.Entry.Title)
				return err
			}
			if r.Transcript != nil {
				_, err = fmt.Fprint(cmd.OutOrStdout(), r.Transcript.Text)
				return err
			}
			return nil
		}
		if r.VoiceStatus != nil {
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "enabled=%t active=%t continuous=%t state=%s namespace=%s segments=%d last_note_id=%s last_error=%q\n", r.VoiceStatus.Enabled, r.VoiceStatus.Active, r.VoiceStatus.Continuous, r.VoiceStatus.State, r.VoiceStatus.Namespace, r.VoiceStatus.Segments, r.VoiceStatus.LastNoteID, r.VoiceStatus.LastError)
			return err
		}
		return nil
	}}
	cmd.Flags().DurationVar(&timeout, "timeout", 8*time.Minute, "whole request deadline")
	cmd.Flags().StringVar(&output, "output", "text", "output: text|json")
	if action == "listen" || action == "enable" {
		cmd.Flags().DurationVar(&silence, "silence", 0, "silence duration before ending each utterance")
		cmd.Flags().DurationVar(&maxDuration, "max-duration", 0, "maximum duration per utterance (150ms to 5m)")
		cmd.Flags().Float64Var(&threshold, "threshold", 0, "speech detection threshold")
		cmd.Flags().StringVar(&device, "device", "", "PipeWire capture target")
		cmd.Flags().StringVar(&mode, "mode", "raw", "note mode: raw|ai")
		cmd.Flags().StringVar(&provider, "provider", "", "configured AI provider name")
		cmd.Flags().StringVar(&title, "title", "", "explicit saved title")
		cmd.Flags().StringVar(&model, "model", "", "installed whisper.cpp GGML model path")
		cmd.Flags().StringVar(&language, "language", "", "STT language code or auto")
		cmd.Flags().StringArrayVar(&tags, "tag", nil, "tag override (repeatable)")
		cmd.Flags().BoolVar(&forceNew, "new", false, "force a new AI note")
		cmd.Flags().StringVar(&note, "note", "", "expand this note ID")
		cmd.MarkFlagsMutuallyExclusive("new", "note")
	}
	if action == "listen" {
		cmd.Flags().DurationVar(&startTimeout, "start-timeout", 0, "wait for initial speech before timing out")
		cmd.Flags().BoolVar(&save, "save", false, "save the transcript as a note")
	}
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
