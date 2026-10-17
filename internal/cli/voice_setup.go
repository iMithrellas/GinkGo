package cli

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/mithrel/ginkgo/internal/config"
	"github.com/mithrel/ginkgo/internal/voiceassets"
	"github.com/mithrel/ginkgo/internal/voiceworker"
)

func newVoiceSetupCmd() *cobra.Command {
	var checkOnly bool
	cmd := &cobra.Command{
		Use: "setup", Short: "Install and verify the standard voice model profile",
		Long: `Install missing, checksum-pinned Parakeet, KWS and Silero assets (about 2.5 GB).
Installed assets and explicit configuration overrides are reused and verified.
Requires the native ginkgo-voice bundle and PipeWire's pw-record; binaries are
discovered on PATH, beside GinkGo, or in standard bin directories.

Verification initializes the native models without opening the microphone.
Setup does not enable voice or edit configuration. After setup, set voice.enabled
to true and voice.mode to raw or ai, restart the daemon, then run voice stream --save.
Use --check for offline verification without downloading or writing assets.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer cancel()
			v := getApp(cmd).Cfg
			helper, err := voiceassets.Executable(v.GetString("voice.sherpa.executable"))
			if err != nil {
				return err
			}
			recorder, err := voiceassets.Executable(v.GetString("voice.capture.executable"))
			if err != nil {
				return err
			}
			cfg, err := config.SherpaConfig(v, recorder, v.GetString("voice.capture.target"), v.GetString("voice.sherpa.keyword"), true)
			if err != nil {
				return err
			}
			clientConfig, err := config.SherpaClientConfig(v, helper)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Voice profile:", voiceassets.Profile)
			if err := voiceassets.Provision(ctx, config.SherpaAssets(v, helper), checkOnly, cmd.OutOrStdout()); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Verifying native compatibility and compiled wake word...")
			if err := voiceworker.New(clientConfig).Verify(ctx, cfg); err != nil {
				return fmt.Errorf("native voice verification: %w; check the native bundle/libraries, model overrides and compiled keyword label", err)
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "Voice setup verified. Enable voice.enabled, choose voice.mode (raw|ai), restart the daemon, then run: ginkgo-cli voice stream --save")
			return err
		},
	}
	cmd.Flags().BoolVar(&checkOnly, "check", false, "verify installed assets and native compatibility without downloads")
	return cmd
}
