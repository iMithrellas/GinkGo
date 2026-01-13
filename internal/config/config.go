package config

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/viper"
)

// applyDefaults seeds Viper with defaults defined in GetConfigOptions.
// This centralizes default values and descriptions in one place.
func applyDefaults(v *viper.Viper) {
	for _, o := range GetConfigOptions() {
		v.SetDefault(o.Key, o.Default)
	}
}

// Load resolves configuration with precedence: defaults < file < env.
// The provided Viper instance is mutated with defaults, file contents, and env.
func Load(ctx context.Context, v *viper.Viper) error {
	// Configure Viper search paths. If SetConfigFile was provided upstream,
	// it takes precedence; these paths are harmless fallbacks.
	if v.ConfigFileUsed() == "" {
		v.SetConfigName("config")
		if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
			v.AddConfigPath(filepath.Join(xdg, "ginkgo"))
		}
		if home, err := os.UserHomeDir(); err == nil {
			v.AddConfigPath(filepath.Join(home, ".config", "ginkgo"))
		}
		v.AddConfigPath(".")
	}

	// Apply centralized defaults (lowest precedence)
	applyDefaults(v)

	// Read config file if present (overrides defaults)
	_ = v.ReadInConfig()

	// Environment variables: GINKGO_* (highest among these sources)
	v.SetEnvPrefix("ginkgo")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	// Normalize a few dependent values post-merge
	if v.GetString("data_dir") == "" {
		v.Set("data_dir", defaultDataDir())
	}
	if strings.TrimSpace(v.GetString("namespace")) == "" {
		v.Set("namespace", "default")
	}

	// Allow comma-separated env override for default_tags
	if len(v.GetStringSlice("default_tags")) == 0 {
		if s := strings.TrimSpace(v.GetString("default_tags")); s != "" {
			parts := strings.Split(s, ",")
			out := make([]string, 0, len(parts))
			for _, p := range parts {
				t := strings.TrimSpace(p)
				if t != "" {
					out = append(out, t)
				}
			}
			if len(out) > 0 {
				v.Set("default_tags", out)
			}
		}
	}
	return nil
}

// defaultDataDir resolves default data dir: $XDG_DATA_HOME/ginkgo or ~/.local/share/ginkgo
func defaultDataDir() string {
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		return filepath.Join(xdg, "ginkgo")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "ginkgo")
}

// DefaultConfigPath resolves the standard config.toml location.
func DefaultConfigPath() string {
	xdg := os.Getenv("XDG_CONFIG_HOME")
	if xdg == "" {
		home, _ := os.UserHomeDir()
		xdg = filepath.Join(home, ".config")
	}
	return filepath.Join(xdg, "ginkgo", "config.toml")
}

type ConfigOption struct {
	Key     string
	Default any
	Comment string
}

// DefaultDBPath builds the default sqlite DB path from data_dir rules.
func DefaultDBPath() string {
	dir := defaultDataDir()
	return filepath.Join(dir, "ginkgo.db")
}

// GetConfigOptions returns the default configuration options and their meanings.
func GetConfigOptions() []ConfigOption {
	return []ConfigOption{
		// Core paths and conventions
		{Key: "data_dir", Default: defaultDataDir(), Comment: "Directory for local state; DB is data_dir/ginkgo.db"},
		{Key: "namespace", Default: "default", Comment: "Default namespace used when none is specified"},
		{Key: "default_tags", Default: []string{}, Comment: "Tags applied when creating a note without explicit tags"},

		{Key: "http_addr", Default: ":8080", Comment: "HTTP listen address for daemon/replication server"},
		{Key: "auth.token", Default: "", Comment: "Shared token required by replication server"},
		{Key: "sync.batch_size", Default: 256, Comment: "Batch size for remote sync operations"},
		{Key: "remotes", Default: map[string]any{}, Comment: "Named remotes: [remotes.<name>] url/token/enabled"},
		{Key: "namespaces", Default: map[string]any{}, Comment: "Per-namespace settings: [namespaces.<name>] e2ee/key_provider/key_id/read_key/write_key/signer_key_provider/signer_key_id/origin_label/trusted_signers"},
		{Key: "export.page_size", Default: 200, Comment: "Batch size for list/search export paging"},
		{Key: "tui.buffer_ratio", Default: 2.0, Comment: "TUI paging buffer ratio; increases the safe window before refetch (0.4-4)"},

		{Key: "notifications.enabled", Default: false, Comment: "Enable reminder notifications"},
		{Key: "notifications.every_days", Default: 3, Comment: "Reminder cadence in days"},
		{Key: "editor.delete_empty", Default: true, Comment: "Delete note if editor exits with no content"},
	}
}

// ResolveDBPath uses Config.DataDir and defaults to return the sqlite DB file path.
func ResolveDBPath(v *viper.Viper) string {
	dir := v.GetString("data_dir")
	if dir == "" {
		dir = defaultDataDir()
	}
	// Expand ~ for convenience
	if len(dir) > 0 && dir[0] == '~' {
		if home, err := os.UserHomeDir(); err == nil {
			dir = filepath.Join(home, dir[1:])
		}
	}
	return filepath.Join(dir, "ginkgo.db")
}

// CheckConfigValidity validates configuration values for common mistakes.
func CheckConfigValidity(v *viper.Viper) error {
	issues := make([]string, 0)

	if strings.TrimSpace(v.GetString("namespace")) == "" {
		issues = append(issues, "namespace is required")
	}
	if strings.TrimSpace(v.GetString("data_dir")) == "" {
		issues = append(issues, "data_dir is required")
	}
	if v.GetInt("export.page_size") <= 0 {
		issues = append(issues, "export.page_size must be greater than 0")
	}
	if v.GetInt("sync.batch_size") <= 0 {
		issues = append(issues, "sync.batch_size must be greater than 0")
	}
	if v.GetBool("notifications.enabled") && v.GetInt("notifications.every_days") <= 0 {
		issues = append(issues, "notifications.every_days must be greater than 0")
	}

	remotes := v.GetStringMap("remotes")
	for name := range remotes {
		base := "remotes." + name + "."
		urlValue := strings.TrimSpace(v.GetString(base + "url"))
		token := strings.TrimSpace(v.GetString(base + "token"))
		enabled := v.GetBool(base + "enabled")
		if enabled || urlValue != "" || token != "" {
			if urlValue == "" {
				issues = append(issues, fmt.Sprintf("remote %s missing url", name))
			} else if _, err := url.ParseRequestURI(urlValue); err != nil {
				issues = append(issues, fmt.Sprintf("remote %s has invalid url", name))
			}
			if token == "" {
				issues = append(issues, fmt.Sprintf("remote %s missing token", name))
			}
		}
	}

	namespaces := v.GetStringMap("namespaces")
	for name := range namespaces {
		base := "namespaces." + name + "."
		e2ee := v.GetBool(base + "e2ee")
		keyProvider := strings.TrimSpace(v.GetString(base + "key_provider"))
		if keyProvider == "" && e2ee {
			keyProvider = "config"
		}
		if keyProvider != "" {
			switch keyProvider {
			case "config":
				readKey := strings.TrimSpace(v.GetString(base + "read_key"))
				writeKey := strings.TrimSpace(v.GetString(base + "write_key"))
				if e2ee || readKey != "" || writeKey != "" {
					if readKey == "" {
						issues = append(issues, fmt.Sprintf("namespace %s missing read_key", name))
					} else if !validBase64Key(readKey) {
						issues = append(issues, fmt.Sprintf("namespace %s read_key must be base64", name))
					}
					if writeKey == "" {
						issues = append(issues, fmt.Sprintf("namespace %s missing write_key", name))
					} else if !validBase64Key(writeKey) {
						issues = append(issues, fmt.Sprintf("namespace %s write_key must be base64", name))
					}
				}
			case "system":
				keyID := strings.TrimSpace(v.GetString(base + "key_id"))
				if keyID == "" {
					issues = append(issues, fmt.Sprintf("namespace %s missing key_id", name))
				}
			default:
				issues = append(issues, fmt.Sprintf("namespace %s has unsupported key_provider %q", name, keyProvider))
			}
		}

		signerProvider := strings.TrimSpace(v.GetString(base + "signer_key_provider"))
		if signerProvider != "" {
			switch signerProvider {
			case "config":
				priv := strings.TrimSpace(v.GetString(base + "signer_priv"))
				if priv == "" {
					issues = append(issues, fmt.Sprintf("namespace %s missing signer_priv", name))
				} else if !validBase64Key(priv) {
					issues = append(issues, fmt.Sprintf("namespace %s signer_priv must be base64", name))
				}
				pub := strings.TrimSpace(v.GetString(base + "signer_pub"))
				if pub != "" && !validBase64Key(pub) {
					issues = append(issues, fmt.Sprintf("namespace %s signer_pub must be base64", name))
				}
			case "system":
				keyID := strings.TrimSpace(v.GetString(base + "signer_key_id"))
				if keyID == "" {
					issues = append(issues, fmt.Sprintf("namespace %s missing signer_key_id", name))
				}
			default:
				issues = append(issues, fmt.Sprintf("namespace %s has unsupported signer_key_provider %q", name, signerProvider))
			}
		}
	}

	if len(issues) == 0 {
		return nil
	}
	return fmt.Errorf("config validation failed:\n- %s", strings.Join(issues, "\n- "))
}

func validBase64Key(value string) bool {
	_, err := base64.StdEncoding.DecodeString(value)
	return err == nil
}
