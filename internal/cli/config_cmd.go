package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
			if _, err := f.WriteString(renderDefaultTOML()); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Wrote %s\n", out)
			return nil
		},
	}
	cmd.Flags().StringVarP(&out, "output", "o", "", "output path for config.toml")
	return cmd
}

func renderDefaultTOML() string {
	var b strings.Builder
	b.WriteString("# GinkGo configuration (TOML)\n")
	opts := config.GetConfigOptions()
	// flat keys
	for _, o := range opts {
		if strings.Contains(o.Key, ".") {
			continue
		}
		if o.Comment != "" {
			b.WriteString("# " + o.Comment + "\n")
		}
		switch v := o.Value.(type) {
		case string:
			b.WriteString(fmt.Sprintf("%s = \"%s\"\n\n", o.Key, v))
		case bool, int, int64:
			b.WriteString(fmt.Sprintf("%s = %v\n\n", o.Key, v))
		case []string:
			b.WriteString(fmt.Sprintf("%s = [", o.Key))
			for i, s := range v {
				if i > 0 {
					b.WriteString(", ")
				}
				b.WriteString(fmt.Sprintf("\"%s\"", s))
			}
			b.WriteString("]\n\n")
		}
	}
	// notifications
	b.WriteString("[notifications]\n")
	for _, o := range opts {
		if strings.HasPrefix(o.Key, "notifications.") {
			k := strings.TrimPrefix(o.Key, "notifications.")
			if o.Comment != "" {
				b.WriteString("# " + o.Comment + "\n")
			}
			b.WriteString(fmt.Sprintf("%s = %v\n", k, o.Value))
		}
	}
	b.WriteString("\n[editor]\n")
	for _, o := range opts {
		if strings.HasPrefix(o.Key, "editor.") {
			k := strings.TrimPrefix(o.Key, "editor.")
			if o.Comment != "" {
				b.WriteString("# " + o.Comment + "\n")
			}
			b.WriteString(fmt.Sprintf("%s = %v\n", k, o.Value))
		}
	}
	b.WriteString("\n")
	return b.String()
}
