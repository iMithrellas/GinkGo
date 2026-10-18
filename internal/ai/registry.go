package ai

import (
	"context"
	"errors"
	"sync"
	"time"
)

// ErrRegistryClosed indicates the daemon-owned provider lifetime has ended.
var ErrRegistryClosed = errors.New("AI provider registry closed")

// Registry lazily shares providers by a configuration identity, not display name.
// Constructor failures are never cached. Identity must include credentials and
// all transport/model settings; callers should use an opaque digest for secrets.
type Registry struct {
	mu      sync.Mutex
	entries map[string]*managedProvider
	closed  bool
	done    chan struct{}
	err     error
	ttl     time.Duration
}

// NewRegistry creates a lazy provider owner with the given metadata cache TTL.
// Nonpositive TTLs select five minutes. Call Close at daemon shutdown.
func NewRegistry(metadataTTL time.Duration) *Registry {
	if metadataTTL <= 0 {
		metadataTTL = 5 * time.Minute
	}
	return &Registry{entries: make(map[string]*managedProvider), ttl: metadataTTL, done: make(chan struct{})}
}

// Resolve serializes construction so concurrent callers share a single owner.
// Constructors must be lazy: discovery belongs in Capabilities, never here.
func (r *Registry) Resolve(identity string, create func() (Provider, error)) (Provider, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, ErrRegistryClosed
	}
	if p := r.entries[identity]; p != nil {
		return p, nil
	}
	p, err := create()
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, ErrProviderUnavailable
	}
	ctx, cancel := context.WithCancel(context.Background())
	m := &managedProvider{provider: p, root: ctx, cancel: cancel, ttl: r.ttl}
	r.entries[identity] = m
	return m, nil
}

// Invalidate retires a configuration owner and joins it before allowing reuse.
// Metadata-only expiration is automatic and does not interrupt active work.
func (r *Registry) Invalidate(identity string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return ErrRegistryClosed
	}
	if p := r.entries[identity]; p != nil {
		delete(r.entries, identity)
		p.stop()
		return p.close()
	}
	return nil
}

// Close is idempotent and joins active inference and discovery before returning.
func (r *Registry) Close() error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		<-r.done
		return r.err
	}
	r.closed = true
	entries := r.entries
	r.entries = nil
	r.mu.Unlock()
	var err error
	for _, p := range entries {
		p.stop()
	}
	for _, p := range entries {
		err = errors.Join(err, p.close())
	}
	r.mu.Lock()
	r.err = err
	close(r.done)
	r.mu.Unlock()
	return err
}

type managedProvider struct {
	provider Provider
	root     context.Context
	cancel   context.CancelFunc
	mu       sync.Mutex
	closed   bool
	wg       sync.WaitGroup
	capMu    capabilityGate
	caps     Capabilities
	expires  time.Time
	ttl      time.Duration
}

func (p *managedProvider) begin(parent context.Context) (context.Context, func(), error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil, nil, ErrRegistryClosed
	}
	p.wg.Add(1)
	ctx, cancel := context.WithCancel(parent)
	stop := context.AfterFunc(p.root, cancel)
	return ctx, func() { stop(); cancel(); p.wg.Done() }, nil
}
func (p *managedProvider) Complete(parent context.Context, req Request) (Response, error) {
	ctx, done, err := p.begin(parent)
	if err != nil {
		return Response{}, err
	}
	defer done()
	return p.provider.Complete(ctx, req)
}

func (p *managedProvider) PreflightImages(parent context.Context, req Request) error {
	ctx, done, err := p.begin(parent)
	if err != nil {
		return err
	}
	defer done()
	if preflight, ok := p.provider.(ImageRequestPreflighter); ok {
		return preflight.PreflightImages(ctx, req)
	}
	return ctx.Err()
}
func (p *managedProvider) Capabilities(parent context.Context) (Capabilities, error) {
	ctx, done, err := p.begin(parent)
	if err != nil {
		return Capabilities{ImageInput: Unknown}, err
	}
	defer done()
	if err := p.capMu.lock(ctx); err != nil {
		return Capabilities{ImageInput: Unknown}, err
	}
	defer p.capMu.unlock()
	if err := ctx.Err(); err != nil {
		return Capabilities{ImageInput: Unknown}, err
	}
	if time.Now().Before(p.expires) {
		return cloneCapabilities(p.caps), nil
	}
	caps, err := ProviderCapabilities(ctx, p.provider)
	if err == nil && ctx.Err() == nil && !caps.Uncacheable {
		p.caps = cloneCapabilities(caps)
		p.expires = time.Now().Add(p.ttl)
	}
	return caps, err
}
func cloneCapabilities(c Capabilities) Capabilities {
	c.ImageMediaTypes = append([]string(nil), c.ImageMediaTypes...)
	return c
}
func (p *managedProvider) stop() { p.mu.Lock(); p.closed = true; p.cancel(); p.mu.Unlock() }
func (p *managedProvider) close() error {
	var err error
	if c, ok := p.provider.(ProviderCloser); ok {
		err = c.Close()
	}
	p.wg.Wait()
	return err
}
