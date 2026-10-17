package config

import (
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/spf13/viper"
)

// VoiceFeatureEnabled preserves explicit switches; omitted switches follow voice.enabled.
func VoiceFeatureEnabled(v *viper.Viper, key string) bool {
	if v.IsSet(key) {
		return v.GetBool(key)
	}
	return v.GetBool("voice.enabled")
}

// VoiceStreamBackend selects the native profile by default, retaining implicit
// Whisper configurations that already opted into its local HTTP server.
func VoiceStreamBackend(v *viper.Viper) string {
	backend := v.GetString("voice.stream.backend")
	if backend == "" || backend == "auto" {
		if v.GetBool("voice.stream.allow_local_http") {
			return "whisper"
		}
		return "sherpa"
	}
	return backend
}

func validateStreamEnabled(v *viper.Viper, key string) error {
	if !v.GetBool("voice.enabled") || !VoiceFeatureEnabled(v, key) {
		return nil
	}
	backend := VoiceStreamBackend(v)
	if backend == "sherpa" {
		if !VoiceFeatureEnabled(v, "voice.events.enabled") {
			return fmt.Errorf("%s requires voice.events.enabled", key)
		}
		return nil // Installed models/executables are checked only on an enabled request.
	}
	if backend != "" && backend != "whisper" {
		return fmt.Errorf("voice.stream.backend must be auto, whisper or sherpa")
	}
	if !VoiceFeatureEnabled(v, "voice.events.enabled") || !v.GetBool("voice.stream.allow_local_http") {
		return fmt.Errorf("%s requires voice.events.enabled and explicit voice.stream.allow_local_http consent", key)
	}
	if strings.TrimSpace(v.GetString("voice.stream.executable")) == "" {
		return fmt.Errorf("voice.stream.executable is required")
	}
	return nil
}

func streamDuration(min, max time.Duration) func(*viper.Viper, string) error {
	return func(v *viper.Viper, key string) error {
		if !v.GetBool("voice.enabled") || !VoiceFeatureEnabled(v, "voice.stream.enabled") {
			return nil
		}
		d, err := time.ParseDuration(v.GetString(key))
		if err != nil || d < min || d > max {
			return fmt.Errorf("%s must be between %s and %s", key, min, max)
		}
		return nil
	}
}

func validateTrigger(v *viper.Viper, key string) error {
	return ValidateTriggerPhrase(v.GetString(key))
}

// ValidateTriggerPhrase checks bounded literal words without probing any device.
func ValidateTriggerPhrase(phrase string) error {
	if len(phrase) > 256 || !utf8.ValidString(phrase) {
		return fmt.Errorf("voice trigger must be valid UTF-8 and at most 256 bytes")
	}
	if strings.TrimSpace(phrase) == "" {
		return nil
	}
	words := 0
	for _, r := range phrase {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			words++
			continue
		}
		if !unicode.IsSpace(r) && !unicode.IsPunct(r) {
			return fmt.Errorf("voice trigger accepts literal words and punctuation only")
		}
		if unicode.IsControl(r) {
			return fmt.Errorf("voice trigger contains control characters")
		}
	}
	if words == 0 {
		return fmt.Errorf("voice trigger requires words")
	}
	return nil
}
