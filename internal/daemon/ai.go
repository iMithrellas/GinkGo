package daemon

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/viper"

	"github.com/mithrel/ginkgo/internal/ai"
)

func resolveAIProvider(v *viper.Viper, name string) (ai.Provider, error) {
	if !v.GetBool("ai.enabled") {
		return nil, fmt.Errorf("AI is disabled")
	}
	if name == "" {
		name = v.GetString("ai.provider")
	}
	if name == "" {
		name = "opencode"
	}
	if strings.ContainsAny(name, ". /\\\r\n\t") {
		return nil, fmt.Errorf("invalid AI provider name")
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
			return nil, fmt.Errorf("ai.timeout must be a positive duration")
		}
	}
	options := ai.Options{Timeout: timeout}
	if kind == "cli" && v.GetString(base+"adapter") == "opencode" {
		kind = "opencode"
	}
	switch kind {
	case "opencode":
		return ai.NewOpenCode(ai.OpenCodeOptions{Options: options, Executable: v.GetString(base + "executable"), Model: v.GetString(base + "model"), EphemeralAuth: v.GetBool(base + "ephemeral_auth")})
	case "opencode-session":
		return ai.NewOpenCodeSession(ai.OpenCodeSessionOptions{Options: options, Executable: v.GetString(base + "executable"), Model: v.GetString(base + "model"), EphemeralAuth: v.GetBool(base + "ephemeral_auth")})
	case "cli":
		return ai.NewCLI(ai.CLIOptions{Options: options, Executable: v.GetString(base + "executable"), Args: v.GetStringSlice(base + "args")})
	case "openai-compatible":
		return ai.NewOpenAI(ai.OpenAIOptions{Options: options, BaseURL: v.GetString(base + "base_url"), Model: v.GetString(base + "model"), APIKey: os.Getenv(v.GetString(base + "api_key_env"))})
	default:
		return nil, fmt.Errorf("unsupported AI provider type for %s", name)
	}
}
