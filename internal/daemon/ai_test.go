package daemon

import (
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"

	"github.com/mithrel/ginkgo/internal/ai"
)

func TestLazyAIProviderConstruction(t *testing.T) {
	v := viper.New()
	v.Set("ai.enabled", false)
	v.Set("ai.timeout", "invalid")
	v.Set("ai.providers.opencode.executable", "/does/not/exist")
	_, err := resolveAIProvider(v, "opencode")
	require.EqualError(t, err, "AI is disabled")
	v.Set("ai.enabled", true)
	v.Set("ai.timeout", "120s")
	p, err := resolveAIProvider(v, "opencode")
	require.NoError(t, err, "constructor must not launch/probe executable")
	require.IsType(t, &ai.OpenCodeProvider{}, p)
	v.Set("ai.providers.opencode.type", "cli")
	v.Set("ai.providers.opencode.adapter", "opencode")
	v.Set("ai.providers.opencode.ephemeral_auth", true)
	p, err = resolveAIProvider(v, "opencode")
	require.NoError(t, err)
	require.IsType(t, &ai.OpenCodeProvider{}, p)
	v.Set("ai.providers.wrapper.type", "cli")
	v.Set("ai.providers.wrapper.executable", "/does/not/exist")
	p, err = resolveAIProvider(v, "wrapper")
	require.NoError(t, err)
	require.IsType(t, &ai.CLIProvider{}, p)
	v.Set("ai.providers.remote.type", "openai-compatible")
	v.Set("ai.providers.remote.base_url", "http://127.0.0.1:1/v1")
	v.Set("ai.providers.remote.model", "test")
	p, err = resolveAIProvider(v, "remote")
	require.NoError(t, err)
	require.IsType(t, &ai.OpenAIProvider{}, p)
	_, err = resolveAIProvider(v, "missing")
	require.Error(t, err)
}
