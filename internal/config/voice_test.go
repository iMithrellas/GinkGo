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
		require.Equal(t, "pw-record", v.GetString("voice.capture.executable"))
		require.Equal(t, "1.5s", v.GetString("voice.capture.silence"))
		require.Equal(t, "2m", v.GetString("voice.capture.max_duration"))
		require.Equal(t, "30s", v.GetString("voice.capture.start_timeout"))
		require.Equal(t, 0.02, v.GetFloat64("voice.capture.threshold"))
		_, changed := UpdateTOML(updated)
		require.False(t, changed)
	}
}
