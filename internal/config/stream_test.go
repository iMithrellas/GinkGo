package config

import (
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
)

func TestStreamDefaultsAndValidation(t *testing.T) {
	v := viper.New()
	applyDefaults(v)
	require.False(t, v.GetBool("voice.stream.enabled"))
	require.False(t, v.GetBool("voice.events.enabled"))
	require.False(t, v.GetBool("voice.stream.allow_local_http"))
	require.Equal(t, "whisper-server", v.GetString("voice.stream.executable"))
	require.Equal(t, "500ms", v.GetString("voice.stream.update_interval"))
	require.NoError(t, CheckConfigValidity(v))
	v.Set("voice.stream.enabled", true)
	v.Set("voice.stream.window", "broken")
	require.NoError(t, CheckConfigValidity(v)) // disabled voice remains isolated
	v.Set("voice.enabled", true)
	require.ErrorContains(t, CheckConfigValidity(v), "consent")
	v.Set("voice.events.enabled", true)
	v.Set("voice.stream.allow_local_http", true)
	v.Set("voice.stream.window", "5s")
	require.NoError(t, CheckConfigValidity(v))
	for _, item := range []struct {
		key   string
		value any
	}{
		{"voice.stream.update_interval", "1ms"}, {"voice.stream.window", "31s"}, {"voice.stream.executable", ""}, {"voice.trigger.phrase", "!!!"}, {"voice.trigger.phrase", "note\x00"},
	} {
		t.Run(item.key+"/"+v.GetString(item.key), func(t *testing.T) {
			old := v.Get(item.key)
			v.Set(item.key, item.value)
			require.Error(t, CheckConfigValidity(v))
			v.Set(item.key, old)
		})
	}
}
