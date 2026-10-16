package config

import (
	"strings"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
)

func TestVoiceDefaultAndUpdatedTOML(t *testing.T) {
	for _, text := range []string{RenderDefaultTOML(), `[voice]
enabled = true
[voice.stt]
model = "/models/ggml-base.bin"
`} {
		updated, _ := UpdateTOML(text)
		v := viper.New()
		v.SetConfigType("toml")
		require.NoError(t, v.ReadConfig(strings.NewReader(updated)))
		require.Equal(t, "raw", v.GetString("voice.mode"))
		require.Equal(t, "on_demand", v.GetString("voice.stt.residency"))
		require.Equal(t, "whisper-cli", v.GetString("voice.stt.executable"))
		require.Equal(t, "auto", v.GetString("voice.stt.device"))
		require.Equal(t, "auto", v.GetString("voice.stt.language"))
		require.Equal(t, 4, v.GetInt("voice.stt.threads"))
		require.Equal(t, "5m", v.GetString("voice.stt.timeout"))
		_, changed := UpdateTOML(updated)
		require.False(t, changed)
	}
}
