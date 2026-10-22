package daemon

import (
	"context"
	"sync"
	"time"

	"github.com/mithrel/ginkgo/internal/voiceworker"
	"github.com/mithrel/ginkgo/pkg/api"
)

// Renew native epochs below the eight-hour sample horizon and 4096-identity
// replay budgets. The daemon job, persistence queue and conversation stay alive.
const voiceEpochDuration = 4 * time.Hour
const voiceEpochIdentities = 2048

func runContinuousVoice(ctx context.Context, runner workerRunner, config voiceworker.Config, stop <-chan struct{}, emit func(voiceworker.Event) error, duration time.Duration, identities int) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		select {
		case <-stop:
			return nil
		default:
		}
		renew, err := runVoiceEpoch(ctx, runner, config, stop, emit, duration, identities)
		if err != nil || !renew {
			return err
		}
	}
}

func runVoiceEpoch(ctx context.Context, runner workerRunner, config voiceworker.Config, stop <-chan struct{}, emit func(voiceworker.Event) error, duration time.Duration, identities int) (bool, error) {
	localStop := make(chan struct{})
	var stopOnce sync.Once
	requestStop := func() { stopOnce.Do(func() { close(localStop) }) }
	var mu sync.Mutex
	active, renew, sealed := false, false, false
	seen := make(map[string]bool)
	prefix := api.NewID() + "/"
	timer := time.NewTimer(duration)
	defer timer.Stop()
	done, joined := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(joined)
		select {
		case <-timer.C:
			mu.Lock()
			renew = true
			if !active {
				requestStop()
			}
			mu.Unlock()
		case <-stop:
			requestStop()
		case <-ctx.Done():
		case <-done:
			return
		}
		// A user stop still wins if renewal was deferred for a selected note.
		select {
		case <-stop:
			requestStop()
		case <-ctx.Done():
		case <-done:
		}
	}()
	err := runner.Run(ctx, config, localStop, func(event voiceworker.Event) error {
		mu.Lock()
		defer mu.Unlock()
		if sealed {
			return context.Canceled
		}
		switch event.Type {
		case "wake":
			active = true
			seen["note:"+event.SessionID] = true
		case "note_candidate", "note_canceled":
			active = false
		case "transcript":
			if event.Transcript != nil {
				seen["caption:"+event.Transcript.UtteranceID] = true
			}
		}
		if len(seen) >= identities {
			renew = true
		}
		// A new helper reuses local IDs and its audio clock. Qualify identities
		// before they enter the persistence/image queues or public events.
		if err := emit(qualifyVoiceEvent(event, prefix)); err != nil {
			return err
		}
		if renew && !active {
			requestStop()
		}
		return nil
	})
	mu.Lock()
	sealed = true
	mu.Unlock()
	close(done)
	<-joined
	return renew, err
}

func qualifyVoiceEvent(event voiceworker.Event, prefix string) voiceworker.Event {
	qualify := func(id string) string {
		if id == "" {
			return ""
		}
		return prefix + id
	}
	event.SessionID = qualify(event.SessionID)
	event.UtteranceID = qualify(event.UtteranceID)
	if event.Transcript != nil {
		copy := *event.Transcript
		copy.UtteranceID = qualify(copy.UtteranceID)
		event.Transcript = &copy
	}
	if event.Candidate != nil {
		copy := *event.Candidate
		copy.SessionID = qualify(copy.SessionID)
		copy.UtteranceID = qualify(copy.UtteranceID)
		event.Candidate = &copy
	}
	return event
}
