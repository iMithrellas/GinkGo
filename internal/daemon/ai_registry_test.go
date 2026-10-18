package daemon

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"

	"github.com/mithrel/ginkgo/internal/ai"
	"github.com/mithrel/ginkgo/internal/imagecontext"
	"github.com/mithrel/ginkgo/internal/ipc"
)

type warmProvider struct{ discoveries atomic.Int32 }

func (p *warmProvider) Complete(context.Context, ai.Request) (ai.Response, error) {
	return ai.Response{}, nil
}
func (p *warmProvider) Capabilities(context.Context) (ai.Capabilities, error) {
	p.discoveries.Add(1)
	return ai.Capabilities{ImageInput: ai.Supported, ResolvedModel: "resolved"}, nil
}

func TestSessionImagesWarmsBeforeWakeAndNeverProbesPerCapture(t *testing.T) {
	r := ai.NewRegistry(time.Minute)
	defer r.Close()
	p := &warmProvider{}
	service := ai.NewService(nil, func(string) (ai.Provider, error) {
		return r.Resolve("native", func() (ai.Provider, error) { return p, nil })
	}, ai.ServiceOptions{Enabled: true, Registry: r})
	require.Zero(t, p.discoveries.Load(), "service construction must not start native sessions")
	v := &voiceRuntime{ai: service, autoScreenshot: true, screenshotMode: imagecontext.Screen}
	var captures atomic.Int32
	v.screenshotResolver = desktopResolver{desktopCaptureFunc(func(context.Context, imagecontext.Mode) ([]imagecontext.Image, error) {
		captures.Add(1)
		return nil, nil
	})}
	raw := newSessionImages(context.Background(), v, ipc.Message{Save: true, Mode: "raw"})
	raw.finish()
	require.Zero(t, p.discoveries.Load(), "raw voice must not probe AI")
	s := newSessionImages(context.Background(), v, ipc.Message{Save: true, Mode: "ai"})
	require.EqualValues(t, 1, p.discoveries.Load(), "warm before any wake/capture")
	s.start("wake-one")
	s.start("wake-two")
	<-s.jobs["wake-one"].done
	<-s.jobs["wake-two"].done
	require.EqualValues(t, 2, captures.Load())
	require.EqualValues(t, 1, p.discoveries.Load())
	s.finish()
}

func TestCachedProviderConfigIdentityAndLifetime(t *testing.T) {
	v := viper.New()
	v.Set("ai.enabled", true)
	v.Set("ai.provider", "test")
	v.Set("ai.providers.test.type", "openai-compatible")
	v.Set("ai.providers.test.base_url", "http://localhost:1/v1")
	v.Set("ai.providers.test.model", "one")
	v.Set("ai.providers.test.api_key_env", "GINKGO_TEST_NATIVE_KEY")
	r := ai.NewRegistry(time.Minute)
	defer r.Close()
	resolve := cachedAIResolver(v, r)
	first, err := resolve("")
	require.NoError(t, err)
	second, err := resolve("test")
	require.NoError(t, err)
	require.Same(t, first, second)
	v.Set("ai.providers.test.model", "two")
	second, err = resolve("")
	require.NoError(t, err)
	require.NotSame(t, first, second)
	t.Setenv("GINKGO_TEST_NATIVE_KEY", "credential-rotation")
	third, err := resolve("")
	require.NoError(t, err)
	require.NotSame(t, second, third)
	v.Set("ai.providers.test.variant", "high")
	fourth, err := resolve("")
	require.NoError(t, err)
	require.NotSame(t, third, fourth)
	require.NoError(t, r.Close())
	_, err = resolve("")
	require.ErrorIs(t, err, ai.ErrRegistryClosed)
}

func TestCachedProviderDisabledDoesNotInitialize(t *testing.T) {
	v := viper.New()
	v.Set("ai.providers.opencode.type", "invalid")
	v.Set("ai.timeout", "invalid")
	r := ai.NewRegistry(time.Minute)
	defer r.Close()
	_, err := cachedAIResolver(v, r)("")
	require.EqualError(t, err, "AI is disabled")
}

func TestCachedResolverIncludesEffectiveEnvironmentModel(t *testing.T) {
	v := viper.New()
	v.SetEnvPrefix("ginkgo")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()
	v.Set("ai.enabled", true)
	v.Set("ai.providers.test.type", "openai-compatible")
	v.Set("ai.providers.test.base_url", "http://localhost:1/v1")
	v.Set("ai.providers.test.image_input", "unsupported")
	v.SetDefault("ai.providers.test.model", "configured-model")
	r := ai.NewRegistry(time.Minute)
	defer r.Close()
	resolve := cachedAIResolver(v, r)
	t.Setenv("GINKGO_AI_PROVIDERS_TEST_MODEL", "first-effective-model")
	first, err := resolve("test")
	require.NoError(t, err)
	snapshot, err := readAIProviderSettings(v, "test")
	require.NoError(t, err)
	t.Setenv("GINKGO_AI_PROVIDERS_TEST_MODEL", "second-effective-model")
	second, err := resolve("test")
	require.NoError(t, err)
	require.NotSame(t, first, second)
	for _, tc := range []struct {
		provider ai.Provider
		model    string
	}{{first, "first-effective-model"}, {second, "second-effective-model"}} {
		caps, err := ai.ProviderCapabilities(context.Background(), tc.provider)
		require.NoError(t, err)
		require.Equal(t, tc.model, caps.ResolvedModel)
	}
	fromSnapshot, err := snapshot.construct()
	require.NoError(t, err)
	caps, err := ai.ProviderCapabilities(context.Background(), fromSnapshot)
	require.NoError(t, err)
	require.Equal(t, "first-effective-model", caps.ResolvedModel, "construction must not reread changed environment")
}
