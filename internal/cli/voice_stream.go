package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/mithrel/ginkgo/internal/config"
	"github.com/mithrel/ginkgo/internal/ipc"
)

func newVoiceStreamCmd() *cobra.Command {
	var save bool
	var trigger, output, mode, provider string
	cmd := &cobra.Command{Use: "stream", Short: "Start live captions with optional wake-gated notes (Sherpa: raw or AI; Whisper: raw)", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if output != "text" && output != "json" {
				return errors.New("--output must be text or json")
			}
			sherpa := config.VoiceStreamBackend(getApp(cmd).Cfg) == "sherpa"
			if !cmd.Flags().Changed("mode") {
				mode = "raw"
				if sherpa && save {
					if configured := getApp(cmd).Cfg.GetString("voice.mode"); configured != "" {
						mode = configured
					}
				}
			}
			if mode != "raw" && mode != "ai" {
				return errors.New("--mode must be raw or ai")
			}
			if !sherpa && mode == "ai" {
				return errors.New("--mode ai requires voice.stream.backend=sherpa; Whisper streams support raw notes only")
			}
			if cmd.Flags().Changed("provider") && (!save || mode != "ai") {
				return errors.New("--provider requires --save and AI mode")
			}
			// Caption-only requests must not carry AI-processing capability.
			if !save {
				mode = "raw"
				provider = ""
			}
			if !cmd.Flags().Changed("trigger") {
				key := "voice.trigger.phrase"
				if sherpa {
					key = "voice.sherpa.keyword"
				}
				trigger = getApp(cmd).Cfg.GetString(key)
			}
			if sherpa {
				if err := config.ValidateKeywordLabel(trigger); err != nil {
					return err
				}
			} else {
				if err := config.ValidateTriggerPhrase(trigger); err != nil {
					return err
				}
				if save && strings.TrimSpace(trigger) == "" {
					return errors.New("--save requires --trigger or voice.trigger.phrase; use voice enable for all-utterance saving")
				}
			}
			ns := resolveNamespace(cmd)
			if save {
				if err := ensureNamespaceConfigured(cmd, ns); err != nil {
					return err
				}
			}
			path, err := ipc.SocketPath()
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()
			r, err := ipc.Request(ctx, path, ipc.Message{Name: "voice.stream", Namespace: ns, Save: save, Trigger: trigger, Mode: mode, Provider: provider})
			if err != nil {
				return err
			}
			if !r.OK {
				return errors.New(r.Msg)
			}
			if r.VoiceStatus == nil {
				return errors.New("missing voice status")
			}
			if output == "json" {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(r)
			}
			message := "Live captions requested (no notes will be saved)"
			if save {
				message = "Wake-gated raw note capture requested"
				if mode == "ai" {
					message = "Wake-gated AI note capture requested (raw fallback if AI is disabled or unavailable)"
				}
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), message+"; inspect readiness with voice status or voice watch --output json; finalize with voice stop or discard pending work with voice cancel.")
			return err
		}}
	cmd.Flags().BoolVar(&save, "save", false, "save selected notes (sherpa: acoustic KWS; whisper: final-confirmed literal prefix)")
	cmd.Flags().StringVar(&mode, "mode", "raw", "saved note mode: raw|ai (Sherpa defaults to voice.mode; Whisper stays raw; captions never use AI)")
	cmd.Flags().StringVar(&provider, "provider", "", "configured AI provider name (requires saving in AI mode; empty uses ai.provider)")
	cmd.Flags().StringVar(&trigger, "trigger", "", "sherpa: compiled keyword label (empty uses configuration; empty configured label selects all); whisper: literal transcript prefix, not acoustic wake")
	cmd.Flags().StringVar(&output, "output", "text", "output: text|json")
	return cmd
}
