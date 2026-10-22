package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mithrel/ginkgo/internal/voiceevents"
	"github.com/mithrel/ginkgo/internal/voiceworker"
)

func TestRenewalPublishesStableStreamAndFreshCaptureEpoch(t *testing.T) {
	hub, err := voiceevents.NewHub(voiceevents.Options{})
	require.NoError(t, err)
	defer hub.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := make(chan struct{})
	v := &voiceRuntime{events: hub}
	ingest := &sherpaIngest{v: v, j: &listenJob{streamID: "stream", stop: stop, cancel: cancel}, ctx: ctx, save: true, pending: map[string]bool{}, namespace: "work", mode: "ai"}
	var snapshots []voiceevents.Event
	epochs := 0
	runner := fakeWorker(func(_ context.Context, _ voiceworker.Config, localStop <-chan struct{}, emit func(voiceworker.Event) error) error {
		epochs++
		if err := emit(voiceworker.Event{Type: "ready", Provider: &voiceworker.ProviderStatus{Requested: voiceworker.CPU, Effective: voiceworker.CPU}}); err != nil {
			return err
		}
		if err := emit(voiceworker.Event{Type: "wake", SessionID: "note", UtteranceID: "audio", RecordingNote: true}); err != nil {
			return err
		}
		if err := emit(voiceworker.Event{Type: "note_canceled", SessionID: "note", Code: "empty"}); err != nil {
			return err
		}
		<-localStop
		if epochs == 2 {
			close(stop)
		}
		return emit(voiceworker.Event{Type: "stopped"})
	})
	err = runContinuousVoice(ctx, runner, voiceworker.Config{}, stop, func(e voiceworker.Event) error {
		if err := ingest.emit(e); err != nil {
			return err
		}
		snapshots = append(snapshots, hub.Snapshot())
		return nil
	}, time.Hour, 1)
	require.NoError(t, err)
	require.Equal(t, 2, epochs)
	var starts, recordings []voiceevents.Event
	for _, snapshot := range snapshots {
		require.Equal(t, "stream", snapshot.StreamID)
		require.Equal(t, "work", snapshot.Namespace)
		require.Equal(t, "ai", snapshot.Mode)
		if snapshot.CaptureState == "starting" || snapshot.CaptureState == "renewing" {
			starts = append(starts, snapshot)
			require.False(t, snapshot.Listening)
		}
		if snapshot.RecordingNote {
			recordings = append(recordings, snapshot)
			require.True(t, snapshot.Listening)
		}
	}
	require.GreaterOrEqual(t, len(starts), 2)
	require.Len(t, recordings, 2)
	require.NotEqual(t, recordings[0].CaptureEpochID, recordings[1].CaptureEpochID)
	require.NotEqual(t, recordings[0].ActiveNoteSessionID, recordings[1].ActiveNoteSessionID)
	require.Equal(t, recordings[0].EpochID, recordings[1].EpochID)
	require.Equal(t, "stopping", hub.Snapshot().CaptureState)
}
