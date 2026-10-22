package daemon

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mithrel/ginkgo/internal/voiceworker"
)

func TestContinuousVoiceBeyondLifetimeIdentityBudget(t *testing.T) {
	stop := make(chan struct{})
	count, epochs := 0, 0
	seen := map[string]bool{}
	selected := ""
	runner := fakeWorker(func(ctx context.Context, _ voiceworker.Config, localStop <-chan struct{}, emit func(voiceworker.Event) error) error {
		epochs++
		for i := 0; ; i++ {
			select {
			case <-localStop:
				return emit(voiceworker.Event{Type: "stopped"})
			default:
			}
			id := fmt.Sprintf("note-%d", i)
			if err := emit(voiceworker.Event{Type: "wake", SessionID: id, UtteranceID: id + "-audio", RecordingNote: true}); err != nil {
				return err
			}
			// Renewal must wait for the selected note's final, even after its
			// recording state becomes false while ASR is still running.
			if err := emit(voiceworker.Event{Type: "state", SessionID: id}); err != nil {
				return err
			}
			select {
			case <-localStop:
				return errors.New("renewed before final")
			default:
			}
			if err := emit(voiceworker.Event{Type: "note_candidate", Candidate: &voiceworker.Candidate{SessionID: id, UtteranceID: id + "-audio", Body: "note", Original: "note"}}); err != nil {
				return err
			}
		}
	})
	err := runContinuousVoice(context.Background(), runner, voiceworker.Config{}, stop, func(e voiceworker.Event) error {
		switch e.Type {
		case "wake":
			require.False(t, seen[e.SessionID])
			seen[e.SessionID] = true
			selected = e.SessionID
		case "note_candidate":
			require.Equal(t, selected, e.Candidate.SessionID)
			count++
			if count == 8192 {
				close(stop)
			}
		}
		return nil
	}, time.Hour, 128)
	require.NoError(t, err)
	require.Greater(t, epochs, 1)
	require.GreaterOrEqual(t, count, 8192)
	require.Less(t, count, 8192+128)
}

func TestContinuousVoiceTimeRenewalAndExplicitStop(t *testing.T) {
	stop := make(chan struct{})
	epochs := 0
	runner := fakeWorker(func(ctx context.Context, _ voiceworker.Config, localStop <-chan struct{}, emit func(voiceworker.Event) error) error {
		epochs++
		if epochs == 2 {
			close(stop)
		}
		select {
		case <-localStop:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	err := runContinuousVoice(context.Background(), runner, voiceworker.Config{}, stop, func(voiceworker.Event) error { return nil }, time.Millisecond, 10)
	require.NoError(t, err)
	require.Equal(t, 2, epochs)
}

func TestContinuousVoiceDoesNotRetryFailuresOrStoppedRequest(t *testing.T) {
	failure := errors.New("helper failed")
	for _, canceled := range []bool{false, true} {
		stop := make(chan struct{})
		calls := 0
		if canceled {
			close(stop)
		}
		runner := fakeWorker(func(context.Context, voiceworker.Config, <-chan struct{}, func(voiceworker.Event) error) error {
			calls++
			return failure
		})
		err := runContinuousVoice(context.Background(), runner, voiceworker.Config{}, stop, func(voiceworker.Event) error { return nil }, time.Hour, 10)
		if canceled {
			require.NoError(t, err)
			require.Zero(t, calls)
		} else {
			require.ErrorIs(t, err, failure)
			require.Equal(t, 1, calls)
		}
	}
}

func TestRetiredVoiceEpochRejectsLateCallbacks(t *testing.T) {
	var late func(voiceworker.Event) error
	runner := fakeWorker(func(_ context.Context, _ voiceworker.Config, _ <-chan struct{}, emit func(voiceworker.Event) error) error {
		late = emit
		return nil
	})
	_, err := runVoiceEpoch(context.Background(), runner, voiceworker.Config{}, nil, func(voiceworker.Event) error { return nil }, time.Hour, 10)
	require.NoError(t, err)
	require.ErrorIs(t, late(voiceworker.Event{Type: "wake"}), context.Canceled)
}
