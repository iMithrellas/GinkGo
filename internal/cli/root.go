package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/mithrel/ginkgo/internal/config"
	"github.com/mithrel/ginkgo/internal/wire"
)

type ctxKey string

const appKey ctxKey = "app"

// Execute is the entrypoint: it builds the root cobra.Command
// and calls its Execute() method to run the CLI.
//
// (Note for future me: Execute() is a method on *cobra.Command*.
// I was confused about receivers vs plain functions, so this reminder stays.)
func Execute() error {
	return NewRootCmd().Execute()
}

// NewRootCmd constructs the Cobra root command and wires dependencies.
func NewRootCmd() *cobra.Command {
	var cfgPath string

	cmd := &cobra.Command{
		Use:           "ginkgo-cli",
		Short:         "GinkGo CLI — local-first journaling",
		SilenceUsage:  true, // don't show usage on runtime errors
		SilenceErrors: true, // let main print errors once
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			// Load config with Viper.
			v := viper.New()
			if cfgPath != "" {
				v.SetConfigFile(cfgPath)
			}
			if err := config.Load(cmd.Context(), v); err != nil {
				return err
			}
			// Wire up the app and stash it in context for subcommands.
			app, err := wire.BuildApp(cmd.Context(), v)
			if err != nil {
				return err
			}
			ctx := context.WithValue(cmd.Context(), appKey, app)
			cmd.SetContext(ctx)
			return nil
		},
	}

	cmd.PersistentFlags().StringVar(&cfgPath, "config", "", "path to config file (yaml|toml)")

	cmd.AddCommand(newNoteCmd())
	cmd.AddCommand(newDaemonCmd())
	cmd.AddCommand(newCompletionCmd())
	cmd.AddCommand(newConfigCmd())
	cmd.AddCommand(newImportCmd())

	cmd.Run = func(cmd *cobra.Command, args []string) { _ = cmd.Help() }

	return cmd
}

func getApp(cmd *cobra.Command) *wire.App {
	v := cmd.Context().Value(appKey)
	if v == nil {
		fmt.Fprintln(os.Stderr, "internal error: app not initialized")
		os.Exit(1)
	}
	return v.(*wire.App)
}
