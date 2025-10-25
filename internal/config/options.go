package config

import (
	"os"
	"path/filepath"
)

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
		{Key: "default_namespace", Value: "default", Comment: "Namespace used when none specified by flags"},
		{Key: "default_tags", Value: []string{}, Comment: "Tags applied when creating a note without explicit tags"},

		// Sections (dotted keys for generator convenience)
		{Key: "notifications.enabled", Value: false, Comment: "Enable reminder notifications"},
		{Key: "notifications.every_days", Value: 3, Comment: "Reminder cadence in days"},
		{Key: "editor.delete_empty", Value: true, Comment: "Delete note if editor exits with no content"},
	}
}

// ResolveDBPath uses Config.DataDir and defaults to return the sqlite DB file path.
func ResolveDBPath(c Config) string {
	dir := c.DataDir
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
