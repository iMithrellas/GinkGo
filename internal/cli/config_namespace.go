package cli

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"

	"github.com/mithrel/ginkgo/internal/config"
	"github.com/mithrel/ginkgo/internal/ipc"
	"github.com/mithrel/ginkgo/internal/keys"
)

func newConfigNamespaceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "namespace",
		Short: "Manage namespace configuration",
	}
	cmd.AddCommand(newConfigNamespaceInitCmd())
	cmd.AddCommand(newConfigNamespaceKeyCmd())
	return cmd
}

func newConfigNamespaceInitCmd() *cobra.Command {
	var ns string
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize a namespace config entry",
		RunE: func(cmd *cobra.Command, args []string) error {
			app := getApp(cmd)
			if strings.TrimSpace(ns) == "" {
				ns = app.Cfg.GetString("namespace")
			}
			if strings.TrimSpace(ns) == "" {
				return fmt.Errorf("namespace is required")
			}
			if app.Cfg.IsSet("namespaces." + ns) {
				return fmt.Errorf("namespace %s already configured", ns)
			}
			return initNamespaceConfig(cmd, ns)
		},
	}
	cmd.Flags().StringVarP(&ns, "namespace", "n", "", "namespace to initialize (defaults to current)")
	registerNamespaceCompletion(cmd)
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
	registerNamespaceCompletion(cmd)
	return cmd
}

func ensureNamespaceConfigured(cmd *cobra.Command, ns string) error {
	if strings.TrimSpace(ns) == "" {
		return nil
	}
	app := getApp(cmd)
	if app.Cfg.IsSet("namespaces." + ns) {
		return nil
	}
	if namespaceExists(cmd, ns) {
		return nil
	}
	if !term.IsTerminal(os.Stdin.Fd()) {
		return fmt.Errorf("namespace %s not configured; run `ginkgo-cli config namespace init -n %s`", ns, ns)
	}
	return initNamespaceConfig(cmd, ns)
}

func namespaceExists(cmd *cobra.Command, ns string) bool {
	sock, err := ipc.SocketPath()
	if err != nil {
		return false
	}
	resp, err := ipc.Request(cmd.Context(), sock, ipc.Message{Name: "namespace.list"})
	if err != nil || !resp.OK {
		return false
	}
	for _, existing := range resp.Namespaces {
		if existing == ns {
			return true
		}
	}
	return false
}

func initNamespaceConfig(cmd *cobra.Command, ns string) error {
	app := getApp(cmd)
	e2ee := true
	keyringAvailable := keys.KeyringAvailable()
	useKeyring := keyringAvailable
	keyID := fmt.Sprintf("ginkgo/ns/%s", ns)
	readKey := randBase64Key(32)
	writeKey := randBase64Key(32)

	fields := []huh.Field{
		huh.NewConfirm().Title("Enable E2EE for this namespace?").Value(&e2ee),
	}
	if keyringAvailable {
		fields = append(fields, huh.NewConfirm().Title("Store keys in system keyring?").Value(&useKeyring))
	} else {
		useKeyring = false
	}
	fields = append(fields,
		huh.NewInput().Title("Key ID").Value(&keyID).Validate(func(s string) error {
			if !e2ee || !useKeyring {
				return nil
			}
			if strings.TrimSpace(s) == "" {
				return fmt.Errorf("key id is required")
			}
			return nil
		}),
		huh.NewInput().Title("Read key (base64)").Value(&readKey).Validate(func(s string) error {
			if !e2ee {
				return nil
			}
			return validateBase64Key(s, "read")
		}),
		huh.NewInput().Title("Write key (base64)").Value(&writeKey).Validate(func(s string) error {
			if !e2ee {
				return nil
			}
			return validateBase64Key(s, "write")
		}),
	)

	form := huh.NewForm(huh.NewGroup(fields...))
	if err := form.Run(); err != nil {
		return err
	}

	values := map[string]any{
		"e2ee": e2ee,
	}
	if e2ee {
		if useKeyring {
			if err := storeKeyringPair(keyID, readKey, writeKey); err != nil {
				return err
			}
			values["key_provider"] = "system"
			values["key_id"] = keyID
		} else {
			values["key_provider"] = "config"
			values["read_key"] = strings.TrimSpace(readKey)
			values["write_key"] = strings.TrimSpace(writeKey)
		}
	}

	path, err := resolveConfigPath(cmd, app.Cfg.ConfigFileUsed())
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}

	content, exists, err := readConfigFile(path)
	if err != nil {
		return err
	}
	if !exists {
		content = config.RenderDefaultTOML()
	}
	updated, _ := config.UpsertNamespaceConfig(content, ns, values)
	if exists {
		if _, err := backupConfig(path); err != nil {
			return err
		}
	}
	if err := os.WriteFile(path, []byte(updated), 0o600); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Wrote %s\n", path)
	return nil
}

func resolveConfigPath(cmd *cobra.Command, used string) (string, error) {
	if p, err := cmd.Flags().GetString("config"); err == nil && strings.TrimSpace(p) != "" {
		return p, nil
	}
	if strings.TrimSpace(used) != "" {
		return used, nil
	}
	return config.DefaultConfigPath(), nil
}

func readConfigFile(path string) (string, bool, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		return string(data), true, nil
	}
	if os.IsNotExist(err) {
		return "", false, nil
	}
	return "", false, err
}

func randBase64Key(n int) string {
	buf := make([]byte, n)
	_, _ = rand.Read(buf)
	return base64.StdEncoding.EncodeToString(buf)
}

func validateBase64Key(s, label string) error {
	if strings.TrimSpace(s) == "" {
		return fmt.Errorf("%s key is required", label)
	}
	if _, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s)); err != nil {
		return fmt.Errorf("%s key must be base64", label)
	}
	return nil
}

func storeKeyringPair(keyID, readKey, writeKey string) error {
	if strings.TrimSpace(keyID) == "" {
		return fmt.Errorf("key id is required")
	}
	rb, err := base64.StdEncoding.DecodeString(strings.TrimSpace(readKey))
	if err != nil {
		return fmt.Errorf("read key must be base64")
	}
	wb, err := base64.StdEncoding.DecodeString(strings.TrimSpace(writeKey))
	if err != nil {
		return fmt.Errorf("write key must be base64")
	}
	ks := &keys.KeyringStore{}
	if err := ks.Put(keyID+"/read", rb); err != nil {
		return err
	}
	if err := ks.Put(keyID+"/write", wb); err != nil {
		return err
	}
	return nil
}
