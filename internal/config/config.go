package config

import (
	"context"
	"os"
	"path/filepath"

	"github.com/spf13/viper"
)

// Config models application configuration.
type Config struct {
	DataDir          string            `mapstructure:"data_dir"`
	Namespace        string            `mapstructure:"namespace"`
	DefaultNamespace string            `mapstructure:"default_namespace"`
	Remotes          map[string]string `mapstructure:"remotes"`
	Notifications    struct {
		Enabled bool `mapstructure:"enabled"`
		Every   int  `mapstructure:"every_days"`
	} `mapstructure:"notifications"`
	Editor struct {
		DeleteEmpty bool `mapstructure:"delete_empty"`
	} `mapstructure:"editor"`
	DefaultTags []string `mapstructure:"default_tags"`
}

// DefaultConfig returns a reasonable set of defaults.
func DefaultConfig() Config {
	dataDir := defaultDataDir()
	return Config{
		DataDir:          dataDir,
		Namespace:        "",
		DefaultNamespace: "default",
		Remotes:          map[string]string{},
		Editor: struct {
			DeleteEmpty bool `mapstructure:"delete_empty"`
		}{DeleteEmpty: true},
		DefaultTags: []string{},
	}
}

// Load resolves configuration from file and env.
func Load(ctx context.Context, v *viper.Viper) (Config, error) {
	cfg := DefaultConfig()
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

	// Environment variables: GINKGO_*
	v.SetEnvPrefix("ginkgo")
	v.AutomaticEnv()

	// Read config file if present.
	_ = v.ReadInConfig()

	if err := v.Unmarshal(&cfg); err != nil {
		return Config{}, err
	}
	if cfg.DataDir == "" {
		cfg.DataDir = defaultDataDir()
	}
	if cfg.Namespace == "" && cfg.DefaultNamespace != "" {
		cfg.Namespace = cfg.DefaultNamespace
	}
	return cfg, nil
}

// defaultDataDir resolves default data dir: $XDG_DATA_HOME/ginkgo or ~/.local/share/ginkgo
func defaultDataDir() string {
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		return filepath.Join(xdg, "ginkgo")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "ginkgo")
}
