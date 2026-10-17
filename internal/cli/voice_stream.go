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
	cmd := &cobra.Command{Use: "stream", Short: "Start resident live captions (optional final-confirmed prefix-gated raw notes)", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if output != "text" && output != "json" {
				return errors.New("--output must be text or json")
			}
			if !cmd.Flags().Changed("trigger") {
				trigger = getApp(cmd).Cfg.GetString("voice.trigger.phrase")
			}
			if err := config.ValidateTriggerPhrase(trigger); err != nil {
				return err
			}
			if save && strings.TrimSpace(trigger) == "" {
				return errors.New("--save requires --trigger or voice.trigger.phrase; use voice enable for all-utterance saving")
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
	cmd.Flags().BoolVar(&save, "save", false, "save raw notes only after a final-confirmed leading trigger phrase")
	cmd.Flags().StringVar(&trigger, "trigger", "", "literal transcript-prefix activation phrase (not an acoustic wake model)")
	cmd.Flags().StringVar(&output, "output", "text", "output: text|json")
	return cmd
}
