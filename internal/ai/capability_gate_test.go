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
				require.False(t, caps.Uncacheable)
			}
			require.EqualValues(t, 1, calls.Load())
		})
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
