package config

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/viper"

	"github.com/mithrel/ginkgo/internal/ai"
)

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

	applyDefaults(v)

	_ = v.ReadInConfig()

	v.SetEnvPrefix("ginkgo")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

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

// ConfigOption describes a supported setting and its default.
type ConfigOption struct {
	Key     string
	Default any
	Comment string
	// Fields describes relative keys in each named table. These defaults are
	// documentation only: dependent defaults are resolved by the consumer.
	Fields []ConfigOption
	// Validate checks a setting after defaults and overrides have been applied.
	Validate func(*viper.Viper, string) error
}

// DefaultDBPath builds the default sqlite DB path from data_dir rules.
func DefaultDBPath() string {
	dir := defaultDataDir()
	return filepath.Join(dir, "ginkgo.db")
}

// GetConfigOptions returns the default configuration options and their meanings.
func GetConfigOptions() []ConfigOption {
	return []ConfigOption{
		{Key: "data_dir", Default: defaultDataDir(), Comment: "Directory for local state; DB is data_dir/ginkgo.db", Validate: requiredString},
		{Key: "namespace", Default: "default", Comment: "Default namespace used when none is specified", Validate: requiredString},
		{Key: "default_tags", Default: []string{}, Comment: "Tags applied when creating a note without explicit tags"},

		{Key: "http_addr", Default: ":8080", Comment: "HTTP listen address for daemon/replication server"},
		{Key: "auth.token", Default: "", Comment: "Shared token required by replication server"},
		{Key: "sync.batch_size", Default: 256, Comment: "Batch size for remote sync operations", Validate: positiveInt},
		{Key: "sync.interval", Default: "60s", Comment: "Interval between background sync operations"},
		{Key: "sync.max_backoff", Default: "10m", Comment: "Maximum background sync retry backoff"},
		{Key: "remotes", Default: map[string]any{}, Comment: "Named remotes", Fields: remoteFields()},
		{Key: "namespaces", Default: map[string]any{}, Comment: "Per-namespace settings", Fields: namespaceFields()},
		{Key: "export.page_size", Default: 200, Comment: "Batch size for list/search export paging", Validate: positiveInt},
		{Key: "tui.buffer_ratio", Default: 2.0, Comment: "TUI paging buffer ratio; increases the safe window before refetch (0.4-4)"},

		{Key: "notifications.enabled", Default: false, Comment: "Enable reminder notifications"},
		{Key: "notifications.every_days", Default: 3, Comment: "Reminder cadence in days", Validate: func(v *viper.Viper, key string) error {
			if v.GetBool("notifications.enabled") {
				return positiveInt(v, key)
			}
			return nil
		}},
		{Key: "editor.delete_empty", Default: true, Comment: "Delete note if editor exits with no content"},
		{Key: "ai.enabled", Default: false, Comment: "Opt in to AI notes; disabled requests save raw fallback notes without launching providers"},
		{Key: "voice.enabled", Default: false, Comment: "Opt in to local transcription and microphone capture; no device/engine/model probes while disabled"},
		{Key: "voice.events.enabled", Default: false, Comment: "Serve private read-only NDJSON voice events; subscribing never starts audio or models"},
		{Key: "voice.stream.enabled", Default: false, Comment: "Opt in to explicit voice stream resident captions; requires voice.enabled, events and local HTTP consent", Validate: validateStreamEnabled},
		{Key: "voice.stream.executable", Default: "whisper-server", Comment: "Installed whisper.cpp server for stream only; legacy whisper-cli settings stay unchanged"},
		{Key: "voice.stream.allow_local_http", Default: false, Comment: "Explicit trust consent for unauthenticated loopback whisper-server; unsafe on untrusted multi-user hosts"},
		{Key: "voice.stream.update_interval", Default: "500ms", Comment: "Rolling preview cadence (10ms-5s)", Validate: streamDuration(10*time.Millisecond, 5*time.Second)},
		{Key: "voice.stream.window", Default: "5s", Comment: "Rolling preview audio window (150ms-30s); revisions replace, never append", Validate: streamDuration(150*time.Millisecond, 30*time.Second)},
		{Key: "voice.trigger.phrase", Default: "", Comment: "Literal leading transcript phrase for stream --save; final confirmation, not acoustic activation", Validate: validateTrigger},
		{Key: "voice.capture.executable", Default: "pw-record", Comment: "PipeWire capture executable"},
		{Key: "voice.capture.target", Default: "", Comment: "Default PipeWire capture target"},
		{Key: "voice.capture.silence", Default: "1.5s", Comment: "Silence duration before one-shot capture ends"},
		{Key: "voice.capture.max_duration", Default: "2m", Comment: "Maximum one-shot capture duration"},
		{Key: "voice.capture.start_timeout", Default: "30s", Comment: "Maximum wait for capture startup"},
		{Key: "voice.capture.threshold", Default: 0.02, Comment: "Speech detection threshold"},
		{Key: "voice.mode", Default: "raw", Comment: "Saved note mode: raw or ai; AI failure preserves raw voice input"},
		{Key: "voice.stt.executable", Default: "whisper-cli", Comment: "User-installed whisper.cpp CLI; no automatic downloads"},
		{Key: "voice.stt.model", Default: "", Comment: "Absolute path to an installed whisper.cpp GGML model"},
		{Key: "voice.stt.language", Default: "auto", Comment: "Auto detection or lowercase two/three-letter language code"},
		{Key: "voice.stt.device", Default: "auto", Comment: "auto or cpu; explicit gpu is unsupported by this POC"},
		{Key: "voice.stt.threads", Default: 4, Comment: "Whisper worker threads (1-256)"},
		{Key: "voice.stt.timeout", Default: "5m", Comment: "Transcription timeout including serialized queue wait"},
		{Key: "voice.stt.residency", Default: "on_demand", Comment: "Only on_demand is supported; persistent and idle_timeout are not implemented"},
		{Key: "ai.provider", Default: "opencode", Comment: "Default configured provider name"},
		{Key: "ai.timeout", Default: "120s", Comment: "Provider execution timeout; invalid provider settings fall back to raw notes"},
		{Key: "ai.notes.context_limit", Default: 8, Comment: "Maximum recent/relevant notes supplied as context"},
		{Key: "ai.notes.system_prompt", Default: ai.DefaultNoteSystemPrompt, Comment: "System prompt controlling AI note cleanup, terminology correction and formatting"},
		{Key: "ai.providers", Default: map[string]any{}, Comment: "Named AI providers. OpenCode saved auth and session mode need >=2.0.25; stateless ephemeral_auth=true supports API-key mode on 2.0.19", Fields: providerFields()},
	}
}

// ResolveDBPath uses Config.DataDir and defaults to return the sqlite DB file path.
func ResolveDBPath(v *viper.Viper) string {
	dir := v.GetString("data_dir")
	if dir == "" {
		dir = defaultDataDir()
	}
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

	for _, option := range GetConfigOptions() {
		if option.Validate != nil {
			if err := option.Validate(v, option.Key); err != nil {
				issues = append(issues, err.Error())
			}
		}
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
