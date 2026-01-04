package cli

import (
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mithrel/ginkgo/internal/keys"
)

func newConfigNamespaceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "namespace",
		Short: "Manage namespace configuration",
	}
	cmd.AddCommand(newConfigNamespaceKeyCmd())
	return cmd
}

func newConfigNamespaceKeyCmd() *cobra.Command {
	var ns string
	cmd := &cobra.Command{
		Use:   "key",
		Short: "Show namespace keys",
		RunE: func(cmd *cobra.Command, args []string) error {
			app := getApp(cmd)
			if strings.TrimSpace(ns) == "" {
				ns = app.Cfg.GetString("namespace")
			}
			if strings.TrimSpace(ns) == "" {
				return fmt.Errorf("namespace is required")
			}

			provider := strings.TrimSpace(app.Cfg.GetString("namespaces." + ns + ".key_provider"))
			keyID := strings.TrimSpace(app.Cfg.GetString("namespaces." + ns + ".key_id"))
			if provider == "" {
				provider = "config"
			}

			readKey := ""
			writeKey := ""
			switch provider {
			case "system":
				if keyID == "" {
					return fmt.Errorf("namespace %s missing key_id for key_provider=system", ns)
				}
				ks := &keys.KeyringStore{}
				rb, rerr := ks.Get(keyID + "/read")
				wb, werr := ks.Get(keyID + "/write")
				if rerr == nil {
					readKey = base64.StdEncoding.EncodeToString(rb)
				}
				if werr == nil {
					writeKey = base64.StdEncoding.EncodeToString(wb)
				}
			case "config":
				readKey = strings.TrimSpace(app.Cfg.GetString("namespaces." + ns + ".read_key"))
				writeKey = strings.TrimSpace(app.Cfg.GetString("namespaces." + ns + ".write_key"))
			default:
				return fmt.Errorf("unsupported key_provider %q for namespace %s", provider, ns)
			}

			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "namespace: %s\n", ns)
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "key_provider: %s\n", provider)
			if keyID != "" {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "key_id: %s\n", keyID)
			}
			if readKey == "" {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "read_key: (missing)")
			} else {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "read_key: %s\n", readKey)
			}
			if writeKey == "" {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "write_key: (missing)")
			} else {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "write_key: %s\n", writeKey)
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&ns, "namespace", "n", "", "namespace to inspect (defaults to current)")
	return cmd
}
