package config

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
)

func TestMinimalVoiceConfigAndLegacySwitches(t *testing.T) {
	v := viper.New()
	applyDefaults(v)
	require.False(t, VoiceFeatureEnabled(v, "voice.stream.enabled"))
	require.False(t, VoiceFeatureEnabled(v, "voice.events.enabled"))
	v.Set("voice.enabled", true)
	require.NoError(t, CheckConfigValidity(v))
	require.True(t, VoiceFeatureEnabled(v, "voice.stream.enabled"))
	require.True(t, VoiceFeatureEnabled(v, "voice.events.enabled"))
	require.Equal(t, "sherpa", VoiceStreamBackend(v))
	v.Set("voice.stream.enabled", false)
	v.Set("voice.events.enabled", false)
	require.False(t, VoiceFeatureEnabled(v, "voice.stream.enabled"))
	require.False(t, VoiceFeatureEnabled(v, "voice.events.enabled"))
	v.Set("voice.stream.allow_local_http", true)
	require.Equal(t, "whisper", VoiceStreamBackend(v), "retain legacy implicit Whisper streams")
	v.Set("voice.stream.backend", "sherpa")
	require.Equal(t, "sherpa", VoiceStreamBackend(v), "explicit backend always wins")
}

func TestVoiceAdvancedSettingsStayRegisteredButNotGenerated(t *testing.T) {
	generated := RenderDefaultTOML()
	parsed := viper.New()
	parsed.SetConfigType("toml")
	require.NoError(t, parsed.ReadConfig(strings.NewReader(generated)))
	for _, option := range GetConfigOptions() {
		if option.Advanced {
			require.False(t, parsed.IsSet(option.Key), option.Key)
		}
	}
	legacy := `[voice]
enabled = true
mode = "ai"
[voice.events]
enabled = false
[voice.stream]
enabled = false
backend = "whisper"
window = "8s"
[voice.sherpa]
executable = "/custom/helper"
provider = "cpu"
keywords_file = "/custom/wake.txt"
silero_model = "/custom/vad.onnx"
pre_roll = "700ms"
[voice.sherpa.parakeet]
encoder = "/custom/encoder.onnx"
[voice.sherpa.kws]
tokens = "/custom/tokens.txt"
`
	updated, _ := UpdateTOML(legacy)
	require.NotContains(t, updated, "OUTDATED")
	require.NotContains(t, updated, "shutdown_timeout =")
	parsed = viper.New()
	parsed.SetConfigType("toml")
	applyDefaults(parsed)
	require.NoError(t, parsed.ReadConfig(strings.NewReader(updated)))
	require.False(t, VoiceFeatureEnabled(parsed, "voice.events.enabled"))
	require.False(t, VoiceFeatureEnabled(parsed, "voice.stream.enabled"))
	require.Equal(t, "whisper", VoiceStreamBackend(parsed))
	require.Equal(t, "/custom/helper", parsed.GetString("voice.sherpa.executable"))
	require.Equal(t, "700ms", parsed.GetString("voice.sherpa.pre_roll"))
	cfg, err := SherpaConfig(parsed, "/custom/recorder", "", "", true)
	require.NoError(t, err)
	require.Equal(t, "/custom/encoder.onnx", cfg.Parakeet.Encoder)
	require.Equal(t, "/custom/tokens.txt", cfg.KWS.Tokens)
	require.Equal(t, "/custom/vad.onnx", cfg.SileroModel)
	require.Equal(t, "/custom/wake.txt", cfg.KeywordsFile)
	_, changed := UpdateTOML(updated)
	require.False(t, changed)
}

func TestVoiceEnvironmentOverridesDiscoveryAndFeatureDefaults(t *testing.T) {
	t.Setenv("GINKGO_VOICE_ENABLED", "true")
	t.Setenv("GINKGO_VOICE_EVENTS_ENABLED", "false")
	t.Setenv("GINKGO_VOICE_SHERPA_PARAKEET_ENCODER", "/env/encoder.onnx")
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	require.NoError(t, os.WriteFile(path, []byte("[voice.sherpa.parakeet]\nencoder = '/file/encoder.onnx'\n"), 0600))
	v := viper.New()
	v.SetConfigFile(path)
	require.NoError(t, Load(context.Background(), v))
	require.True(t, VoiceFeatureEnabled(v, "voice.stream.enabled"))
	require.False(t, VoiceFeatureEnabled(v, "voice.events.enabled"))
	cfg, err := SherpaConfig(v, "/installed/recorder", "", "", false)
	require.NoError(t, err)
	require.Equal(t, "/env/encoder.onnx", cfg.Parakeet.Encoder)
}
