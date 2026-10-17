package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mithrel/ginkgo/internal/ipc"
	"github.com/spf13/cobra"

	"github.com/mithrel/ginkgo/internal/config"
)

func newVoiceStreamCmd() *cobra.Command {
	var save bool
	var trigger, output string
	cmd := &cobra.Command{Use: "stream", Short: "Start live captions with optional backend-specific wake-gated raw notes", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if output != "text" && output != "json" {
				return errors.New("--output must be text or json")
			}
			sherpa := getApp(cmd).Cfg.GetString("voice.stream.backend") == "sherpa"
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
			r, err := ipc.Request(ctx, path, ipc.Message{Name: "voice.stream", Namespace: ns, Save: save, Trigger: trigger, Mode: "raw"})
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
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "Live captions started; subscribe with voice watch --output json; stop with voice stop or discard with voice cancel.")
			return err
		}}
	cmd.Flags().BoolVar(&save, "save", false, "save selected raw notes (sherpa: acoustic KWS; whisper: final-confirmed literal prefix)")
	cmd.Flags().StringVar(&trigger, "trigger", "", "sherpa: compiled keyword label (empty uses configuration; empty configured label selects all); whisper: literal transcript prefix, not acoustic wake")
	cmd.Flags().StringVar(&output, "output", "text", "output: text|json")
	return cmd
}
