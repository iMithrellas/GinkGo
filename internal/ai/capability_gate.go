package ai

import (
	"context"
	"sync"
)

// capabilityGate serializes discovery while allowing queued callers to leave.
// Its zero value is ready for use.
type capabilityGate struct {
	once sync.Once
	slot chan struct{}
}

func (g *capabilityGate) lock(ctx context.Context) error {
	g.once.Do(func() { g.slot = make(chan struct{}, 1) })
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case g.slot <- struct{}{}:
		if err := ctx.Err(); err != nil {
			g.unlock()
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (g *capabilityGate) unlock() { <-g.slot }
