package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/viper"

	"github.com/mithrel/ginkgo/internal/voiceworker"
)

// ValidateKeywordLabel accepts an installed compiled keyword label, not a phrase.
// Empty selects every keyword in the installed keywords file.
func ValidateKeywordLabel(label string) error {
	if len(label) > 128 {
		return fmt.Errorf("sherpa keyword label must be at most 128 bytes")
	}
	for _, r := range label {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("_.-", r)) {
			return fmt.Errorf("sherpa --trigger selects a compiled keyword label (letters, digits, _, ., -), not an arbitrary phrase")
		}
	}
	return nil
}

// SherpaClientConfig resolves bounded supervisor budgets without native probes.
func SherpaClientConfig(v *viper.Viper, helper string) (voiceworker.ClientConfig, error) {
	c := voiceworker.ClientConfig{Executable: helper}
	for key, dest := range map[string]*time.Duration{"startup_timeout": &c.StartupTimeout, "shutdown_timeout": &c.ShutdownTimeout} {
		d, err := time.ParseDuration(v.GetString("voice.sherpa." + key))
		if err != nil || d < time.Millisecond || d > 5*time.Minute {
			return c, fmt.Errorf("voice.sherpa.%s must be between 1ms and 5m", key)
		}
		*dest = d
	}
	return c, nil
}

// SherpaConfig resolves trusted settings without filesystem or native probes.
// Call only for an enabled stream request; recorder must already be absolute.
func SherpaConfig(v *viper.Viper, recorder, target, keyword string, save bool) (voiceworker.Config, error) {
	graph := func(key string) voiceworker.Graph {
		return voiceworker.Graph{Encoder: v.GetString(key + ".encoder"), Decoder: v.GetString(key + ".decoder"), Joiner: v.GetString(key + ".joiner"), Tokens: v.GetString(key + ".tokens")}
	}
	c := voiceworker.Config{Enabled: true, RecorderExecutable: recorder, RecorderTarget: target,
		Parakeet: graph("voice.sherpa.parakeet"), KWS: graph("voice.sherpa.kws"), KeywordsFile: v.GetString("voice.sherpa.keywords_file"), SileroModel: v.GetString("voice.sherpa.silero_model"),
		Provider: voiceworker.Provider(v.GetString("voice.sherpa.provider")), AllowCPUFallback: v.GetBool("voice.sherpa.cpu_fallback"), RecordingEnabled: save, SelectedKeyword: keyword}
	if err := ValidateKeywordLabel(keyword); err != nil {
		return c, err
	}
	for _, setting := range []struct {
		key      string
		dest     *int
		min, max time.Duration
	}{
		{"voice.stream.update_interval", &c.UpdateMS, 10 * time.Millisecond, 5 * time.Second},
		{"voice.stream.window", &c.WindowMS, 150 * time.Millisecond, 30 * time.Second},
		{"voice.capture.silence", &c.SilenceMS, time.Millisecond, time.Minute},
		{"voice.sherpa.max_recording", &c.MaxRecordingMS, 250 * time.Millisecond, 5 * time.Minute},
		{"voice.sherpa.trigger_timeout", &c.TriggerTimeoutMS, time.Millisecond, 5 * time.Minute},
		{"voice.sherpa.cooldown", &c.CooldownMS, 0, time.Minute},
		{"voice.sherpa.pre_roll", &c.PreRollMS, 0, 30 * time.Second},
	} {
		d, err := time.ParseDuration(v.GetString(setting.key))
		if err != nil || d < setting.min || d > setting.max {
			return c, fmt.Errorf("%s must be between %s and %s", setting.key, setting.min, setting.max)
		}
		*setting.dest = int(d.Milliseconds())
	}
	if c.MaxRecordingMS+max(c.PreRollMS, c.WindowMS)+c.SilenceMS+1000 > 120000 {
		return c, fmt.Errorf("sherpa retained audio requires max_recording + max(pre_roll, window) + silence + 1s <= 120s")
	}
	return c, c.Validate()
}
