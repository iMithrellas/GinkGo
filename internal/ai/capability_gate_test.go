package ai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mithrel/ginkgo/internal/imagecontext"
)

func TestRegistryRetriesExplicitOverrideAfterDiscoveryFailure(t *testing.T) {
	var calls atomic.Int32
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"id":"model","input_modalities":["text"]}`))
	}))
	defer host.Close()
	raw, err := NewOpenAI(OpenAIOptions{BaseURL: host.URL, Model: "model", Options: Options{ImageInput: "supported"}})
	require.NoError(t, err)
	registry := NewRegistry(time.Minute)
	defer registry.Close()
	managed, err := registry.Resolve("config", func() (Provider, error) { return raw, nil })
	require.NoError(t, err)
	first, err := ProviderCapabilities(context.Background(), managed)
	require.NoError(t, err)
	require.Equal(t, Supported, first.ImageInput)
	require.True(t, first.Uncacheable)
	require.Error(t, raw.PreflightImages(context.Background(), Request{Images: []imagecontext.Image{aiImage(t)}}), "recovered metadata must reject images before prompt finalization")
	second, err := ProviderCapabilities(context.Background(), managed)
	require.NoError(t, err)
	require.EqualValues(t, 2, calls.Load())
	require.Equal(t, Unsupported, second.ImageInput)
}

func TestMissingMetadataEndpointDoesNotProbeEveryRequest(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusNotImplemented} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var calls atomic.Int32
			host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(status)
			}))
			defer host.Close()
			provider, err := NewOpenAI(OpenAIOptions{BaseURL: host.URL, Model: "model", Options: Options{ImageInput: "supported"}})
			require.NoError(t, err)
			for range 2 {
				caps, err := provider.Capabilities(context.Background())
				require.NoError(t, err)
				require.Equal(t, Supported, caps.ImageInput)
			}
			require.EqualValues(t, 1, calls.Load())
		})
	}
}

func TestRegistryRespectsProviderMetadataExpiry(t *testing.T) {
	var calls atomic.Int32
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			_, _ = w.Write([]byte(`{"id":"model","input_modalities":["image"]}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"model","input_modalities":["text"]}`))
	}))
	defer host.Close()
	provider, err := NewOpenAI(OpenAIOptions{BaseURL: host.URL, Model: "model"})
	require.NoError(t, err)
	managed := registerTestProvider(t, provider)
	for range 2 {
		caps, err := ProviderCapabilities(context.Background(), managed)
		require.NoError(t, err)
		require.Equal(t, Supported, caps.ImageInput)
	}
	require.EqualValues(t, 1, calls.Load())
	provider.capExpires = time.Time{}
	caps, err := ProviderCapabilities(context.Background(), managed)
	require.NoError(t, err)
	require.Equal(t, Unsupported, caps.ImageInput)
	require.EqualValues(t, 2, calls.Load())
}

func TestOpenAIDiscoveryFailures(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   error
	}{
		{http.StatusUnauthorized, ErrAuthentication},
		{http.StatusForbidden, ErrAuthentication},
		{http.StatusTooManyRequests, ErrRateLimited},
		{http.StatusServiceUnavailable, ErrProviderUnavailable},
	} {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte("secret upstream diagnostics"))
			}))
			defer host.Close()
			provider, err := NewOpenAI(OpenAIOptions{BaseURL: host.URL, Model: "model"})
			require.NoError(t, err)
			_, err = provider.Capabilities(context.Background())
			require.ErrorIs(t, err, tc.want)
			require.NotContains(t, err.Error(), "secret")
		})
	}
}

func TestOpenAIDiscoveryCancellation(t *testing.T) {
	for _, body := range []bool{false, true} {
		name := "headers"
		if body {
			name = "body"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if body {
					w.WriteHeader(http.StatusOK)
					w.(http.Flusher).Flush()
				}
				cancel()
				<-r.Context().Done()
			}))
			defer host.Close()
			provider, err := NewOpenAI(OpenAIOptions{BaseURL: host.URL, Model: "model"})
			require.NoError(t, err)
			_, err = provider.Capabilities(ctx)
			require.ErrorIs(t, err, context.Canceled)
		})
	}
}

func TestOpenAICompletionTimeoutIncludesDiscovery(t *testing.T) {
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("timed-out request reached endpoint")
	}))
	defer host.Close()
	provider, err := NewOpenAI(OpenAIOptions{BaseURL: host.URL, Model: "model", Options: Options{Timeout: 20 * time.Millisecond}})
	require.NoError(t, err)
	require.NoError(t, provider.capMu.lock(context.Background()))
	defer provider.capMu.unlock()
	request := Request{Prompt: "note", Images: []imagecontext.Image{aiImage(t)}}
	done := make(chan error, 1)
	go func() {
		_, err := provider.Complete(context.Background(), request)
		done <- err
	}()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.DeadlineExceeded)
	case <-time.After(time.Second):
		t.Fatal("completion timeout did not bound queued discovery")
	}
}

type queuedCapabilitiesProvider struct{ entered, release chan struct{} }

func (p *queuedCapabilitiesProvider) Complete(context.Context, Request) (Response, error) {
	return Response{}, nil
}
func (p *queuedCapabilitiesProvider) Capabilities(ctx context.Context) (Capabilities, error) {
	close(p.entered)
	select {
	case <-p.release:
		return Capabilities{ImageInput: Supported}, nil
	case <-ctx.Done():
		return Capabilities{}, ctx.Err()
	}
}

func assertQueuedDiscoveryCancellation(t *testing.T, discover func(context.Context) (Capabilities, error), entered, release chan struct{}) {
	t.Helper()
	first := make(chan error, 1)
	go func() { _, err := discover(context.Background()); first <- err }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("first discovery did not start")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	second := make(chan error, 1)
	go func() { _, err := discover(ctx); second <- err }()
	select {
	case err := <-second:
		require.ErrorIs(t, err, context.DeadlineExceeded)
	case <-time.After(time.Second):
		close(release)
		<-first
		t.Fatal("queued discovery ignored cancellation")
	}
	// The canceled caller must return while discovery still owns the gate.
	select {
	case <-first:
		t.Fatal("first discovery ended before release")
	default:
	}
	close(release)
	require.NoError(t, <-first)
}

func TestRegistryQueuedCapabilitiesCancellation(t *testing.T) {
	r := NewRegistry(time.Minute)
	defer r.Close()
	p := &queuedCapabilitiesProvider{entered: make(chan struct{}), release: make(chan struct{})}
	managed, err := r.Resolve("queued", func() (Provider, error) { return p, nil })
	require.NoError(t, err)
	assertQueuedDiscoveryCancellation(t, managed.(CapabilityProvider).Capabilities, p.entered, p.release)
}

func TestOpenAIQueuedCapabilitiesCancellation(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		select {
		case <-release:
			_, _ = w.Write([]byte(`{"id":"model","input_modalities":["image"]}`))
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	p, err := NewOpenAI(OpenAIOptions{BaseURL: server.URL, Model: "model"})
	require.NoError(t, err)
	assertQueuedDiscoveryCancellation(t, p.Capabilities, entered, release)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = p.Capabilities(ctx)
	require.ErrorIs(t, err, context.Canceled, "cached metadata must not hide caller cancellation")
}
