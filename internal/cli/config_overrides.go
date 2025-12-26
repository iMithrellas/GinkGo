package cli

import (
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/mithrel/ginkgo/internal/config"
)

func applyConfigFlagOverrides(cmd *cobra.Command, v *viper.Viper, extra map[string]string) {
	for _, opt := range config.GetConfigOptions() {
		flag := cmd.Flags().Lookup(opt.Key)
		if flag == nil || !flag.Changed {
			continue
		}
		setFromFlag(cmd, v, opt.Key, opt.Key)
	}
	for flagName, key := range extra {
		flag := cmd.Flags().Lookup(flagName)
		if flag == nil || !flag.Changed {
			continue
		}
		setFromFlag(cmd, v, flagName, key)
	}
}

func setFromFlag(cmd *cobra.Command, v *viper.Viper, flagName, key string) {
	switch cmd.Flags().Lookup(flagName).Value.Type() {
	case "bool":
		if val, err := cmd.Flags().GetBool(flagName); err == nil {
			v.Set(key, val)
		}
	case "int":
		if val, err := cmd.Flags().GetInt(flagName); err == nil {
			v.Set(key, val)
		}
	case "int64":
		if val, err := cmd.Flags().GetInt64(flagName); err == nil {
			v.Set(key, val)
		}
	case "stringSlice":
		if val, err := cmd.Flags().GetStringSlice(flagName); err == nil {
			v.Set(key, val)
		}
	default:
		if val, err := cmd.Flags().GetString(flagName); err == nil {
			v.Set(key, val)
		}
	}
}
