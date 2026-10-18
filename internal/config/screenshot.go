package config

import (
	"fmt"

	"github.com/spf13/viper"

	"github.com/mithrel/ginkgo/internal/imagecontext"
)

func screenshotMode(v *viper.Viper, key string) error {
	if !v.IsSet(key) {
		return nil
	}
	if _, err := imagecontext.ParseMode(v.GetString(key)); err != nil {
		return fmt.Errorf("%s: %w", key, err)
	}
	return nil
}

func screenshotBackend(v *viper.Viper, key string) error {
	if !v.IsSet(key) {
		return nil
	}
	value := v.GetString(key)
	if value != "auto" && value != "hyprland" {
		return fmt.Errorf("%s must be auto or hyprland", key)
	}
	return nil
}
