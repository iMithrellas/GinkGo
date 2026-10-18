package imagecontext

import (
	"errors"
	"sync"
	"time"
)

const (
	pendingLifetime      = 2 * time.Minute
	maxPendingNamespaces = 8
)

// Pending holds an expiring image batch for each namespace.
type Pending struct {
	mu      sync.Mutex
	batches map[string]pendingBatch
	now     func() time.Time
}

type pendingBatch struct {
	images  []Image
	expires time.Time
	timer   *time.Timer
}

// NewPending creates an empty pending image store.
func NewPending() *Pending {
	return &Pending{batches: make(map[string]pendingBatch), now: time.Now}
}

func (p *Pending) expire() {
	for ns, batch := range p.batches {
		if !p.now().Before(batch.expires) {
			if batch.timer != nil {
				batch.timer.Stop()
			}
			delete(p.batches, ns)
		}
	}
}

// Stage replaces a namespace's pending images for two minutes.
// Images must be validated before staging.
func (p *Pending) Stage(namespace string, images []Image) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.expire()
	if len(images) == 0 {
		return errors.New("no images to attach")
	}
	if _, exists := p.batches[namespace]; !exists && len(p.batches) >= maxPendingNamespaces {
		return errors.New("too many namespaces with pending images")
	}
	if old := p.batches[namespace]; old.timer != nil {
		old.timer.Stop()
	}
	expires := p.now().Add(pendingLifetime)
	timer := time.AfterFunc(pendingLifetime, func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		if batch, ok := p.batches[namespace]; ok && batch.expires.Equal(expires) {
			delete(p.batches, namespace)
		}
	})
	p.batches[namespace] = pendingBatch{images: images, expires: expires, timer: timer}
	return nil
}

// Take returns and removes a namespace's unexpired images.
func (p *Pending) Take(namespace string) []Image {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.expire()
	batch := p.batches[namespace]
	if batch.timer != nil {
		batch.timer.Stop()
	}
	delete(p.batches, namespace)
	return batch.images
}

// Count returns the number of unexpired images in a namespace.
func (p *Pending) Count(namespace string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.expire()
	return len(p.batches[namespace].images)
}

// Close clears pending images and stops their timers.
func (p *Pending) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for ns, batch := range p.batches {
		if batch.timer != nil {
			batch.timer.Stop()
		}
		delete(p.batches, ns)
	}
}
