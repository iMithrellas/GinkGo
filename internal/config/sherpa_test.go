package config

import (
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
)

func sherpaTestConfig() *viper.Viper {
	v := viper.New()
	applyDefaults(v)
	for _, graph := range []string{"parakeet", "kws"} {
		for _, part := range []string{"encoder", "decoder", "joiner", "tokens"} {
			v.Set("voice.sherpa."+graph+"."+part, "/installed/"+graph+"-"+part)
		}
	}
	v.Set("voice.sherpa.keywords_file", "/installed/keywords.txt")
	v.Set("voice.sherpa.silero_model", "/installed/silero.onnx")
	return v
}

func TestSherpaConfigBoundsAndLegacyIsolation(t *testing.T) {
	v := sherpaTestConfig()
	require.Equal(t, "sherpa", VoiceStreamBackend(v))
	v.Set("voice.enabled", true)
	v.Set("voice.events.enabled", true)
	v.Set("voice.stream.enabled", true)
	v.Set("voice.stream.backend", "sherpa")
	v.Set("voice.stt.device", "invalid-legacy")
	v.Set("voice.stt.residency", "invalid-legacy")
	require.NoError(t, CheckConfigValidity(v), "sherpa needs neither HTTP consent nor Whisper model/device/residency")
	c, err := SherpaConfig(v, "/installed/pw-record", "", "GINKGO", true)
	require.NoError(t, err)
	require.True(t, c.AllowCPUFallback)
	require.True(t, c.RecordingEnabled)
	require.Equal(t, "GINKGO", c.SelectedKeyword)
	require.Equal(t, 60000, c.MaxRecordingMS)
	require.Equal(t, 3000, c.TriggerTimeoutMS, "short Sherpa-only wait must not retain stale wakes")
	require.Equal(t, 500, c.PreRollMS, "retain the verified native selection context")
	require.Equal(t, "30s", v.GetString("voice.capture.start_timeout"), "leave legacy capture waiting unchanged")
	require.Equal(t, "2m", v.GetString("voice.capture.max_duration"), "leave legacy capture duration unchanged")
	for key, value := range map[string]string{"voice.sherpa.max_recording": "2m", "voice.sherpa.trigger_timeout": "1h", "voice.sherpa.pre_roll": "31s", "voice.sherpa.cooldown": "61s", "voice.stream.window": "31s", "voice.sherpa.provider": "gpu"} {
		t.Run(key, func(t *testing.T) {
			v := sherpaTestConfig()
			v.Set(key, value)
			_, err := SherpaConfig(v, "/installed/pw-record", "", "", false)
			require.Error(t, err)
		})
	}
	_, err = SherpaConfig(v, "/installed/pw-record", "", "take note", true)
	require.ErrorContains(t, err, "compiled keyword label")
}

func TestSherpaDisabledConfigDoesNotValidateInstalledPaths(t *testing.T) {
	v := sherpaTestConfig()
	v.Set("voice.stream.backend", "sherpa")
	v.Set("voice.sherpa.executable", "/does/not/exist")
	v.Set("voice.sherpa.provider", "invalid")
	v.Set("voice.sherpa.parakeet.encoder", "relative")
	require.NoError(t, CheckConfigValidity(v))
}

func TestSherpaSupervisorBudgetsAndRingBoundary(t *testing.T) {
	v := sherpaTestConfig()
	c, err := SherpaClientConfig(v, "/installed/helper")
	require.NoError(t, err)
	require.Equal(t, "2m0s", c.StartupTimeout.String())
	require.Equal(t, "30s", c.ShutdownTimeout.String())
	for _, key := range []string{"startup_timeout", "shutdown_timeout"} {
		v.Set("voice.sherpa."+key, "6m")
		_, err := SherpaClientConfig(v, "/installed/helper")
		require.Error(t, err)
		v.Set("voice.sherpa."+key, "30s")
	}
	// Exactly 120s fits; no duration is silently reduced to make it fit.
	v.Set("voice.sherpa.max_recording", "112.5s")
	cfg, err := SherpaConfig(v, "/installed/recorder", "", "", false)
	require.NoError(t, err)
	require.Equal(t, 112500, cfg.MaxRecordingMS)
	v.Set("voice.sherpa.max_recording", "112.501s")
	_, err = SherpaConfig(v, "/installed/recorder", "", "", false)
	require.ErrorContains(t, err, "retained audio")
	v.Set("voice.sherpa.max_recording", "249ms")
	_, err = SherpaConfig(v, "/installed/recorder", "", "", false)
	require.Error(t, err)
}
