package ai

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type registryProvider struct {
	calls    atomic.Int32
	closes   atomic.Int32
	fail     atomic.Bool
	started  chan struct{}
	finished chan struct{}
}

func (p *registryProvider) Complete(ctx context.Context, _ Request) (Response, error) {
	close(p.started)
	defer close(p.finished)
	<-ctx.Done()
	return Response{}, ctx.Err()
}
func (p *registryProvider) Capabilities(context.Context) (Capabilities, error) {
	p.calls.Add(1)
	if p.fail.Load() {
		return Capabilities{}, ErrProviderUnavailable
	}
	return Capabilities{ImageInput: Supported, ResolvedModel: "actual-model", ImageMediaTypes: []string{"image/png"}}, nil
}
func (p *registryProvider) Close() error { p.closes.Add(1); return nil }

func TestRegistryConcurrentIdentityInvalidationAndClose(t *testing.T) {
	r := NewRegistry(time.Minute)
	defer r.Close()
	var creates atomic.Int32
	p := &registryProvider{started: make(chan struct{}), finished: make(chan struct{})}
	create := func() (Provider, error) { creates.Add(1); return p, nil }
	var wg sync.WaitGroup
	providers := make(chan Provider, 32)
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, e := r.Resolve("config-a", create)
			if e == nil {
				providers <- v
			}
		}()
	}
	wg.Wait()
	close(providers)
	first, err := r.Resolve("config-a", create)
	require.NoError(t, err)
	for got := range providers {
		require.Same(t, first, got)
	}
	require.EqualValues(t, 1, creates.Load())
	otherOwner := &registryProvider{}
	other, err := r.Resolve("config-b", func() (Provider, error) { return otherOwner, nil })
	require.NoError(t, err)
	require.NotSame(t, first, other)
	require.NoError(t, r.Invalidate("config-b"))
	require.EqualValues(t, 1, otherOwner.closes.Load())
	replacement, err := r.Resolve("config-b", func() (Provider, error) { return &registryProvider{}, nil })
	require.NoError(t, err)
	require.NotSame(t, other, replacement)
	go first.Complete(context.Background(), Request{})
	<-p.started
	var closers sync.WaitGroup
	for range 8 {
		closers.Add(1)
		go func() { defer closers.Done(); _ = r.Close() }()
	}
	closers.Wait()
	select {
	case <-p.finished:
	default:
		t.Fatal("Close did not join inference")
	}
	require.EqualValues(t, 1, p.closes.Load())
	_, err = r.Resolve("config-a", create)
	require.ErrorIs(t, err, ErrRegistryClosed)
	_, err = first.Complete(context.Background(), Request{})
	require.ErrorIs(t, err, ErrRegistryClosed)
}

func TestRegistryMetadataTTLAndTransientErrors(t *testing.T) {
	r := NewRegistry(time.Minute)
	defer r.Close()
	p := &registryProvider{}
	_, err := r.Resolve("config", func() (Provider, error) { return nil, ErrProviderUnavailable })
	require.ErrorIs(t, err, ErrProviderUnavailable)
	managed, err := r.Resolve("config", func() (Provider, error) { return p, nil })
	require.NoError(t, err)
	p.fail.Store(true)
	_, err = ProviderCapabilities(context.Background(), managed)
	require.ErrorIs(t, err, ErrProviderUnavailable)
	p.fail.Store(false)
	caps, err := ProviderCapabilities(context.Background(), managed)
	require.NoError(t, err)
	require.Equal(t, "actual-model", caps.ResolvedModel)
	caps.ImageMediaTypes[0] = "mutated"
	caps, err = ProviderCapabilities(context.Background(), managed)
	require.NoError(t, err)
	require.Equal(t, []string{"image/png"}, caps.ImageMediaTypes)
	require.EqualValues(t, 2, p.calls.Load())
	m := managed.(*managedProvider)
	require.NoError(t, m.capMu.lock(context.Background()))
	m.expires = time.Time{}
	m.capMu.unlock()
	_, err = ProviderCapabilities(context.Background(), managed)
	require.NoError(t, err)
	require.EqualValues(t, 3, p.calls.Load())
}

func TestEffectiveImageSupportIntersection(t *testing.T) {
	for _, model := range []Support{Unknown, Supported, Unsupported} {
		for _, transport := range []Support{Unknown, Supported, Unsupported} {
			for _, override := range []string{"auto", "supported", "unsupported"} {
				want := Unknown
				if model == Unsupported || transport == Unsupported || override == "unsupported" {
					want = Unsupported
				} else if transport == Supported && (model == Supported || override == "supported") {
					want = Supported
				}
				require.Equal(t, want, EffectiveImageSupport(model, transport, override), "%s/%s/%s", model, transport, override)
			}
		}
	}
}
