package config

import (
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
)

func TestScreenshotConfigValidationHasNoDesktopDependencies(t *testing.T) {
	v := viper.New()
	require.NoError(t, screenshotMode(v, "voice.screenshot.mode"))
	require.NoError(t, screenshotBackend(v, "voice.screenshot.backend"))
	for _, mode := range []string{"window", "screen", "all-screens"} {
		v.Set("voice.screenshot.mode", mode)
		require.NoError(t, screenshotMode(v, "voice.screenshot.mode"))
	}
	v.Set("voice.screenshot.mode", "desktop")
	require.Error(t, screenshotMode(v, "voice.screenshot.mode"))
	for _, backend := range []string{"auto", "hyprland"} {
		v.Set("voice.screenshot.backend", backend)
		require.NoError(t, screenshotBackend(v, "voice.screenshot.backend"))
	}
	v.Set("voice.screenshot.backend", "unsupported")
	require.Error(t, screenshotBackend(v, "voice.screenshot.backend"))
}
