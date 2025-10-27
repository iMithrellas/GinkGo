package cli

import (
	"fmt"

	"github.com/mithrel/ginkgo/internal/daemon"
	"github.com/spf13/cobra"
)

func newDaemonCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Interact with the local daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			app := getApp(cmd) // initialized via PersistentPreRunE
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Starting ginkgo daemon...\n")
			return daemon.Run(cmd.Context(), app)
		},
	}
	return cmd
}
