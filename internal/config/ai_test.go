package config

import (
	"context"
	"strings"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"

	"github.com/mithrel/ginkgo/internal/ai"
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
	require.Equal(t, ai.DefaultNoteSystemPrompt, v.GetString("ai.notes.system_prompt"))
	require.True(t, v.GetBool("ai.providers.opencode.ephemeral_auth"))
	require.Equal(t, []string{"--json"}, v.GetStringSlice("ai.providers.wrapper.args"))
	updated, _ := UpdateTOML(text)
	v = viper.New()
	v.SetConfigType("toml")
	require.NoError(t, v.ReadConfig(strings.NewReader(updated)))
	require.Equal(t, "openai/gpt-5", v.GetString("ai.providers.opencode.model"))
}

func TestRenderDefaultTOMLGroupsOptionsWithoutBlankLines(t *testing.T) {
	text := RenderDefaultTOML()
	for _, section := range []string{"[ai]", "[ai.notes]"} {
		start := strings.Index(text, section+"\n")
		require.NotEqual(t, -1, start, "missing section %s", section)
		rest := text[start+len(section)+1:]
		end := strings.Index(rest, "\n\n[")
		if end < 0 {
			end = len(rest)
		}
		block := strings.TrimRight(rest[:end], "\n")
		require.NotContains(t, block, "\n\n", "blank line inside %s block", section)
	}
	text = strings.ReplaceAll(text, "\n", " ")
	require.Contains(t, text, `system_prompt = "Return exactly one JSON object`)
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
	require.Equal(t, ai.DefaultNoteSystemPrompt, v.GetString("ai.notes.system_prompt"))
	require.Equal(t, "opencode", v.GetString("ai.provider"))
	require.Equal(t, "TEST_API_KEY", v.GetString("ai.providers.test.api_key_env"))
	_, changed = UpdateTOML(updated)
	require.False(t, changed, "update must not repeat sections or dynamic maps")
	require.NotContains(t, updated, "context_limit = 3\n\n# System prompt")
}

func TestAIDisabledConfigDoesNotValidateProviders(t *testing.T) {
	v := viper.New()
	v.Set("ai.providers.broken.type", "unsupported")
	v.Set("ai.timeout", "not-a-duration")
	require.NoError(t, Load(context.Background(), v))
	require.NoError(t, CheckConfigValidity(v))
}

func TestNativeProviderFieldsSurviveConfigUpdate(t *testing.T) {
	text := `[ai]
enabled = true
provider = "native"
[ai.providers.native]
type = "opencode-native"
executable = "/custom/opencode"
model = "provider/model"
variant = "high"
temp_dir = "/private/sessions"
ephemeral_auth = true
image_input = "auto"
[ai.providers.code]
type = "codex"
variant = "medium"
[ai.providers.claude]
type = "claude-code"
`
	updated, _ := UpdateTOML(text)
	v := viper.New()
	v.SetConfigType("toml")
	require.NoError(t, v.ReadConfig(strings.NewReader(updated)))
	require.Equal(t, "opencode-native", v.GetString("ai.providers.native.type"))
	require.Equal(t, "high", v.GetString("ai.providers.native.variant"))
	require.Equal(t, "/private/sessions", v.GetString("ai.providers.native.temp_dir"))
	require.True(t, v.GetBool("ai.providers.native.ephemeral_auth"))
	require.Equal(t, "codex", v.GetString("ai.providers.code.type"))
	require.Equal(t, "claude-code", v.GetString("ai.providers.claude.type"))
}
