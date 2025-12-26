package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/mithrel/ginkgo/internal/config"
	"github.com/spf13/cobra"
)

func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Manage configuration",
	}
	cmd.AddCommand(newConfigGenerateCmd())
	return cmd
}

func newConfigGenerateCmd() *cobra.Command {
	var out string
	cmd := &cobra.Command{
		Use:   "generate",
		Short: "Generate a default config.toml",
		RunE: func(cmd *cobra.Command, args []string) error {
			if out == "" {
				// default path
				xdg := os.Getenv("XDG_CONFIG_HOME")
				if xdg == "" {
					home, _ := os.UserHomeDir()
					xdg = filepath.Join(home, ".config")
				}
				out = filepath.Join(xdg, "ginkgo", "config.toml")
			}
			if err := os.MkdirAll(filepath.Dir(out), 0o700); err != nil {
				return err
			}
			f, err := os.OpenFile(out, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
			if err != nil {
				return err
			}
			defer f.Close()
			if _, err := f.WriteString(config.RenderDefaultTOML()); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Wrote %s\n", out)
			return nil
		},
	}
	cmd.Flags().StringVarP(&out, "output", "o", "", "output path for config.toml")
	return cmd
}
