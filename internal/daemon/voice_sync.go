package daemon

import "context"

// scheduleSync coalesces note commits into one daemon-owned worker and at most
// one pending sync. Device capture and job cancellation never wait for network
// work; close cancels and joins the worker before the store can be closed.
func (v *voiceRuntime) scheduleSync() {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed || v.syncNote == nil {
		return
	}
	if v.syncPending == nil {
		root := v.root
		if root == nil {
			root = context.Background()
		}
		ctx, cancel := context.WithCancel(root)
		v.syncCancel = cancel
		v.syncPending = make(chan struct{}, 1)
		v.syncDone = make(chan struct{})
		go runVoiceSync(ctx, v.syncPending, v.syncDone, v.syncNote)
	}
	select {
	case v.syncPending <- struct{}{}:
	default:
	}
}

func runVoiceSync(ctx context.Context, pending <-chan struct{}, done chan<- struct{}, syncNote func(context.Context)) {
	defer close(done)
	for {
		select {
		case <-ctx.Done():
			return
		case <-pending:
			if ctx.Err() != nil {
				return
			}
			syncNote(ctx)
		}
	}
}
