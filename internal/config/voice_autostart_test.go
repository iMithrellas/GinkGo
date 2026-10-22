package config

import (
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
)

func TestVoiceAutostartValidation(t *testing.T) {
	v := viper.New()
	require.NoError(t, validateVoiceAutostart(v, "voice.autostart"))
	v.Set("voice.autostart", true)
	require.Error(t, validateVoiceAutostart(v, "voice.autostart"))
	v.Set("voice.enabled", true)
	require.NoError(t, validateVoiceAutostart(v, "voice.autostart"))
	for _, key := range []string{"voice.stream.enabled", "voice.events.enabled"} {
		v.Set(key, false)
		require.Error(t, validateVoiceAutostart(v, "voice.autostart"))
		v.Set(key, true)
	}
	v.Set("voice.stream.backend", "whisper")
	require.Error(t, validateVoiceAutostart(v, "voice.autostart"))
	v.Set("voice.stream.backend", "sherpa")
	v.Set("voice.mode", "invalid")
	require.Error(t, validateVoiceAutostart(v, "voice.autostart"))
}
