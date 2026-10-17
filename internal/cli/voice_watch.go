package cli

import (
	"encoding/json"
	"errors"

	"github.com/spf13/cobra"

	"github.com/mithrel/ginkgo/internal/voiceevents"
)

func newVoiceWatchCmd() *cobra.Command {
	var output string
	cmd := &cobra.Command{Use: "watch", Short: "Subscribe to live voice events without starting capture", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if output != "json" {
				return errors.New("--output must be json")
			}
			path, err := voiceevents.SocketPath()
			if err != nil {
				return err
			}
			encoder := json.NewEncoder(cmd.OutOrStdout())
			err = voiceevents.Watch(cmd.Context(), path, func(e voiceevents.Event) error { return encoder.Encode(e) })
			if cmd.Context().Err() != nil {
				return nil
			}
			return err
		},
	}
	cmd.Flags().StringVar(&output, "output", "json", "output: json (NDJSON v1)")
	return cmd
}
