package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/viper"

	"github.com/mithrel/ginkgo/internal/ai"
)

// aiProviderSettings is an effective configuration snapshot. The same values
// define cache identity and construct the owner, including Viper env overrides.
// Credential is never logged; only an opaque digest becomes a registry key.
type aiProviderSettings struct {
	Name, Kind, Executable, Model, Variant, TempDir, AuthHome string
	BaseURL, Credential                                       string
	Args                                                      []string
	EphemeralAuth                                             bool
	Options                                                   ai.Options
}

func readAIProviderSettings(v *viper.Viper, name string) (aiProviderSettings, error) {
	if !v.GetBool("ai.enabled") {
		return aiProviderSettings{}, fmt.Errorf("AI is disabled")
	}
	if name == "" {
		name = v.GetString("ai.provider")
	}
	if name == "" {
		name = "opencode"
	}
	if strings.ContainsAny(name, ". /\\\r\n\t") {
		return aiProviderSettings{}, fmt.Errorf("invalid AI provider name")
	}
	base := "ai.providers." + name + "."
	kind := v.GetString(base + "type")
	if kind == "" && name == "opencode" {
		kind = "opencode"
	}
	timeout := 120 * time.Second
	if v.IsSet("ai.timeout") {
		var err error
		timeout, err = time.ParseDuration(v.GetString("ai.timeout"))
		if err != nil || timeout <= 0 {
			return aiProviderSettings{}, fmt.Errorf("ai.timeout must be a positive duration")
		}
	}
	options := ai.Options{Timeout: timeout, ImageInput: v.GetString(base + "image_input")}
	if kind == "cli" && v.GetString(base+"adapter") == "opencode" {
		kind = "opencode"
	}
	return aiProviderSettings{
		Name: name, Kind: kind, Options: options,
		Executable: v.GetString(base + "executable"), Model: v.GetString(base + "model"),
		Variant: v.GetString(base + "variant"), TempDir: v.GetString(base + "temp_dir"),
		AuthHome: v.GetString(base + "auth_home"), EphemeralAuth: v.GetBool(base + "ephemeral_auth"),
		Args: append([]string(nil), v.GetStringSlice(base+"args")...), BaseURL: v.GetString(base + "base_url"),
		Credential: os.Getenv(v.GetString(base + "api_key_env")),
	}, nil
}

func (s aiProviderSettings) construct() (ai.Provider, error) {
	switch s.Kind {
	case "opencode-native":
		return ai.NewOpenCodeNative(ai.OpenCodeNativeOptions{Options: s.Options, Executable: s.Executable, Model: s.Model, Variant: s.Variant, TempDir: s.TempDir, EphemeralAuth: s.EphemeralAuth})
	case "codex":
		return ai.NewCodex(ai.CodexOptions{Options: s.Options, Executable: s.Executable, Model: s.Model, Variant: s.Variant, TempDir: s.TempDir, AuthHome: s.AuthHome})
	case "claude-code":
		return ai.NewClaudeCode(ai.ClaudeCodeOptions{Options: s.Options, Executable: s.Executable, Model: s.Model, Variant: s.Variant, TempDir: s.TempDir})
	case "opencode":
		return ai.NewOpenCode(ai.OpenCodeOptions{Options: s.Options, Executable: s.Executable, Model: s.Model, EphemeralAuth: s.EphemeralAuth})
	case "opencode-session":
		return ai.NewOpenCodeSession(ai.OpenCodeSessionOptions{Options: s.Options, Executable: s.Executable, Model: s.Model, EphemeralAuth: s.EphemeralAuth})
	case "cli":
		return ai.NewCLI(ai.CLIOptions{Options: s.Options, Executable: s.Executable, Args: s.Args})
	case "openai-compatible":
		return ai.NewOpenAI(ai.OpenAIOptions{Options: s.Options, BaseURL: s.BaseURL, Model: s.Model, APIKey: s.Credential})
	default:
		return nil, fmt.Errorf("unsupported AI provider type for %s", s.Name)
	}
}

func resolveAIProvider(v *viper.Viper, name string) (ai.Provider, error) {
	settings, err := readAIProviderSettings(v, name)
	if err != nil {
		return nil, err
	}
	return settings.construct()
}

// cachedAIResolver includes the complete provider configuration and current
// credential value in an opaque identity. Changing either creates a fresh owner;
// previous owners remain managed until daemon shutdown so in-flight work is safe.
func cachedAIResolver(v *viper.Viper, registry *ai.Registry) func(string) (ai.Provider, error) {
	return func(name string) (ai.Provider, error) {
		settings, err := readAIProviderSettings(v, name)
		if err != nil {
			return nil, err
		}
		identity, err := json.Marshal(settings)
		if err != nil {
			return nil, fmt.Errorf("invalid AI provider configuration")
		}
		digest := sha256.Sum256(identity)
		return registry.Resolve(hex.EncodeToString(digest[:]), settings.construct)
	}
}
