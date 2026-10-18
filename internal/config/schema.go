package config

import (
	"fmt"
	"strings"

	"github.com/spf13/viper"
)

func requiredString(v *viper.Viper, key string) error {
	if strings.TrimSpace(v.GetString(key)) == "" {
		return fmt.Errorf("%s is required", key)
	}
	return nil
}

func positiveInt(v *viper.Viper, key string) error {
	if v.GetInt(key) <= 0 {
		return fmt.Errorf("%s must be greater than 0", key)
	}
	return nil
}

func remoteFields() []ConfigOption {
	return []ConfigOption{
		{Key: "url", Default: "", Comment: "Replication server URL"},
		{Key: "token", Default: "", Comment: "Shared replication authentication token"},
		{Key: "enabled", Default: true, Comment: "Enable this remote; defaults to enabled when a URL is configured"},
	}
}

func namespaceFields() []ConfigOption {
	return []ConfigOption{
		{Key: "e2ee", Default: false, Comment: "Enable end-to-end encryption"},
		{Key: "key_provider", Default: "", Comment: "config or system; defaults to config when E2EE is enabled"},
		{Key: "key_id", Default: "", Comment: "System keyring encryption key identifier"},
		{Key: "read_key", Default: "", Comment: "Base64 encryption read key for config provider"},
		{Key: "write_key", Default: "", Comment: "Base64 encryption write key for config provider"},
		{Key: "signer_key_provider", Default: "", Comment: "Signing key provider: config or system"},
		{Key: "signer_key_id", Default: "", Comment: "System keyring signing key identifier"},
		{Key: "signer_priv", Default: "", Comment: "Base64 private signing key for config provider"},
		{Key: "signer_pub", Default: "", Comment: "Optional base64 public signing key"},
		{Key: "origin_label", Default: "", Comment: "Origin label; defaults to hostname"},
		{Key: "trusted_signers", Default: []string{}, Comment: "Trusted base64 public signing keys"},
	}
}

func providerFields() []ConfigOption {
	return []ConfigOption{
		{Key: "type", Default: "", Comment: "opencode, opencode-session, cli or openai-compatible; defaults to opencode for the provider named opencode"},
		{Key: "executable", Default: "", Comment: "Provider executable; OpenCode providers default to opencode"},
		{Key: "model", Default: "", Comment: "Provider model identifier"},
		{Key: "image_input", Default: "auto", Comment: "Selected model image support: auto (metadata, unknown skips), supported, unsupported; adapter must also accept images"},
		{Key: "args", Default: []string{}, Comment: "Arguments for CLI providers"},
		{Key: "adapter", Default: "", Comment: "Legacy CLI adapter: opencode selects the OpenCode provider"},
		{Key: "base_url", Default: "", Comment: "OpenAI-compatible API base URL"},
		{Key: "api_key_env", Default: "", Comment: "Environment variable containing the API key"},
		{Key: "ephemeral_auth", Default: false, Comment: "Use ephemeral OpenCode API-key authentication"},
	}
}
