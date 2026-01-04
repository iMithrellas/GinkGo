package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/mithrel/ginkgo/internal/config"
	"github.com/spf13/cobra"
)

func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Manage configuration",
	}
	cmd.AddCommand(newConfigGenerateCmd())
	cmd.AddCommand(newConfigNamespaceCmd())
	return cmd
}

func newConfigGenerateCmd() *cobra.Command {
	var out string
	var overwrite bool
	var update bool
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
			if overwrite && update {
				return fmt.Errorf("choose either --overwrite or --update")
			}
			return writeConfigFile(cmd, out, overwrite, update)
		},
	}
	cmd.Flags().StringVarP(&out, "output", "o", "", "output path for config.toml")
	cmd.Flags().BoolVar(&overwrite, "overwrite", false, "overwrite existing config (creates a backup)")
	cmd.Flags().BoolVar(&update, "update", false, "merge defaults into existing config (creates a backup)")
	return cmd
}

func writeConfigFile(cmd *cobra.Command, out string, overwrite, update bool) error {
	if err := os.MkdirAll(filepath.Dir(out), 0o700); err != nil {
		return err
	}

	exists := fileExists(out)
	if exists && !overwrite && !update {
		return fmt.Errorf("config already exists at %s; use --overwrite to replace (this will delete your current config) or --update to merge defaults", out)
	}

	content := ""
	if update && exists {
		data, err := os.ReadFile(out)
		if err != nil {
			return err
		}
		updated, changed := config.UpdateTOML(string(data))
		if !changed {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Config already up to date: %s\n", out)
			return nil
		}
		content = updated
	} else {
		content = config.RenderDefaultTOML()
	}

	var backupPath string
	if exists && (overwrite || update) {
		var err error
		backupPath, err = backupConfig(out)
		if err != nil {
			return err
		}
	}

	if err := os.WriteFile(out, []byte(content), 0o600); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Wrote %s\n", out)
	if backupPath != "" {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Backup: %s\n", backupPath)
	}
	return nil
}

func backupConfig(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	backup := path + ".bak"
	if fileExists(backup) {
		backup = fmt.Sprintf("%s.bak-%s", path, time.Now().Format("20060102-150405"))
	}
	if err := os.WriteFile(backup, data, 0o600); err != nil {
		return "", err
	}
	return backup, nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
