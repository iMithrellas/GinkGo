package config

import (
	"context"
	"strings"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
)

func TestAIDefaultsAndNestedTOML(t *testing.T) {
	text := RenderDefaultTOML() + `
[ai.providers.opencode]
type = "opencode"
executable = "opencode"
model = "openai/gpt-5"
ephemeral_auth = true

[ai.providers.wrapper]
type = "cli"
args = ["--json"]
executable = "/usr/local/bin/wrapper"
`
	v := viper.New()
	v.SetConfigType("toml")
	require.NoError(t, v.ReadConfig(strings.NewReader(text)))
	require.False(t, v.GetBool("ai.enabled"))
	require.Equal(t, "opencode", v.GetString("ai.provider"))
	require.Equal(t, "120s", v.GetString("ai.timeout"))
	require.Equal(t, 8, v.GetInt("ai.notes.context_limit"))
	require.True(t, v.GetBool("ai.providers.opencode.ephemeral_auth"))
	require.Equal(t, []string{"--json"}, v.GetStringSlice("ai.providers.wrapper.args"))
	updated, _ := UpdateTOML(text)
	v = viper.New()
	v.SetConfigType("toml")
	require.NoError(t, v.ReadConfig(strings.NewReader(updated)))
	require.Equal(t, "openai/gpt-5", v.GetString("ai.providers.opencode.model"))
}

func TestAIConfigUpdateExistingNestedSections(t *testing.T) {
	text := `[ai]
enabled = true
[ai.notes]
context_limit = 3
[ai.providers.test]
type = "openai-compatible"
base_url = "http://localhost:8081/v1"
model = "test"
api_key_env = "TEST_API_KEY"
`
	updated, changed := UpdateTOML(text)
	require.True(t, changed)
	v := viper.New()
	v.SetConfigType("toml")
	require.NoError(t, v.ReadConfig(strings.NewReader(updated)))
	require.Equal(t, 3, v.GetInt("ai.notes.context_limit"))
	require.Equal(t, "opencode", v.GetString("ai.provider"))
	require.Equal(t, "TEST_API_KEY", v.GetString("ai.providers.test.api_key_env"))
	_, changed = UpdateTOML(updated)
	require.False(t, changed, "update must not repeat sections or dynamic maps")
}

func TestAIDisabledConfigDoesNotValidateProviders(t *testing.T) {
	v := viper.New()
	v.Set("ai.providers.broken.type", "unsupported")
	v.Set("ai.timeout", "not-a-duration")
	require.NoError(t, Load(context.Background(), v))
	require.NoError(t, CheckConfigValidity(v))
}
