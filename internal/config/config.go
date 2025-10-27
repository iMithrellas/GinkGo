package config

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/viper"
)

// applyDefaults seeds Viper with defaults defined in GetConfigOptions.
// This centralizes default values and descriptions in one place.
func applyDefaults(v *viper.Viper) {
	for _, o := range GetConfigOptions() {
		v.SetDefault(o.Key, o.Value)
	}
}

// Load resolves configuration with precedence: defaults < file < env.
// The provided Viper instance is mutated with defaults, file contents, and env.
func Load(ctx context.Context, v *viper.Viper) error {
	// Configure Viper search paths. If SetConfigFile was provided upstream,
	// it takes precedence; these paths are harmless fallbacks.
	v.SetConfigName("config")
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		v.AddConfigPath(filepath.Join(xdg, "ginkgo"))
	}
	if home, err := os.UserHomeDir(); err == nil {
		v.AddConfigPath(filepath.Join(home, ".config", "ginkgo"))
	}
	v.AddConfigPath(".")

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
	if v.GetString("namespace") == "" && v.GetString("default_namespace") != "" {
		v.Set("namespace", v.GetString("default_namespace"))
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

// No typed accessors: call viper directly in callers, e.g.,
// v.GetString("namespace"), v.GetStringSlice("default_tags").

// defaultDataDir resolves default data dir: $XDG_DATA_HOME/ginkgo or ~/.local/share/ginkgo
func defaultDataDir() string {
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		return filepath.Join(xdg, "ginkgo")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "ginkgo")
}

type ConfigOption struct {
	Key     string
	Value   any
	Comment string
}

// DefaultDBPath builds the default sqlite DB path from data_dir rules.
func DefaultDBPath() string {
	dir := defaultDataDir()
	return filepath.Join(dir, "ginkgo.db")
}

// GetConfigOptions returns the default configuration options and their meanings.
// This is the single source of truth for default values and generator output.
func GetConfigOptions() []ConfigOption {
	return []ConfigOption{
		// Core paths and conventions
		{Key: "data_dir", Value: defaultDataDir(), Comment: "Directory for local state; DB is data_dir/ginkgo.db"},
		{Key: "default_namespace", Value: "default", Comment: "Default namespace used when none is specified"},
		{Key: "namespace", Value: "", Comment: "Current namespace; if empty, falls back to default_namespace"},
		{Key: "default_tags", Value: []string{}, Comment: "Tags applied when creating a note without explicit tags"},

		// Sections (dotted keys for generator convenience)
		{Key: "notifications.enabled", Value: false, Comment: "Enable reminder notifications"},
		{Key: "notifications.every_days", Value: 3, Comment: "Reminder cadence in days"},
		{Key: "editor.delete_empty", Value: true, Comment: "Delete note if editor exits with no content"},
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
