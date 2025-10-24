package config

import (
    "context"
    "fmt"
    "os"
    "path/filepath"

    "github.com/spf13/viper"
)

// Config models application configuration.
type Config struct {
    DataDir       string            `mapstructure:"data_dir"`
    DBURL         string            `mapstructure:"db_url"`
    Namespace     string            `mapstructure:"namespace"`
    Remotes       map[string]string `mapstructure:"remotes"`
    Notifications struct {
        Enabled bool `mapstructure:"enabled"`
        Every   int  `mapstructure:"every_days"`
    } `mapstructure:"notifications"`
}

// DefaultConfig returns a reasonable set of defaults.
func DefaultConfig() Config {
    home, _ := os.UserHomeDir()
    dataDir := filepath.Join(home, ".local", "share", "ginkgo")
    return Config{
        DataDir:   dataDir,
        DBURL:     fmt.Sprintf("sqlite://%s", filepath.Join(dataDir, "ginkgo.db")),
        Namespace: "default",
        Remotes:   map[string]string{},
    }
}

// Load resolves configuration from file and env.
func Load(ctx context.Context, v *viper.Viper) (Config, error) {
    cfg := DefaultConfig()

    // Configure Viper search paths if no explicit file provided.
    if v.ConfigFileUsed() == "" && v.ConfigFile() == "" {
        v.SetConfigName("config")
        v.SetConfigType("yaml")
        // XDG default
        if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
            v.AddConfigPath(filepath.Join(xdg, "ginkgo"))
        }
        // Fallback
        if home, err := os.UserHomeDir(); err == nil {
            v.AddConfigPath(filepath.Join(home, ".config", "ginkgo"))
        }
        v.AddConfigPath(".")
    }

    // Environment variables: GINKGO_*
    v.SetEnvPrefix("ginkgo")
    v.AutomaticEnv()

    // Read config file if present.
    _ = v.ReadInConfig()

    if err := v.Unmarshal(&cfg); err != nil {
        return Config{}, err
    }
    return cfg, nil
}

