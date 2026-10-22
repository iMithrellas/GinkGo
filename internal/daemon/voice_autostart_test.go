package daemon

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mithrel/ginkgo/internal/voiceevents"
	"github.com/mithrel/ginkgo/internal/voiceworker"
)

func TestVoiceAutostartOptInAndStop(t *testing.T) {
	v, log, _ := sherpaRuntime(t, "unused")
	var starts atomic.Int32
	v.sherpaSettings.Set("namespace", "work")
	v.sherpaSettings.Set("voice.mode", "raw")
	v.sherpaSettings.Set("voice.stream.backend", "sherpa")
	v.sherpaRunner = fakeWorker(func(ctx context.Context, _ voiceworker.Config, stop <-chan struct{}, emit func(voiceworker.Event) error) error {
		starts.Add(1)
		if err := emit(voiceworker.Event{Type: "ready", Provider: &voiceworker.ProviderStatus{Requested: voiceworker.CPU, Effective: voiceworker.CPU}}); err != nil {
			return err
		}
		select {
		case <-stop:
			return emit(voiceworker.Event{Type: "stopped"})
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	require.NoError(t, v.autostartVoice(context.Background()))
	require.Zero(t, starts.Load())
	require.False(t, v.voiceStatus().VoiceStatus.Active)
	v.sherpaSettings.Set("voice.autostart", true)
	require.NoError(t, v.autostartVoice(context.Background()))
	log.wait(t, func(e voiceevents.Event) bool { return e.Listening })
	require.Equal(t, "work", v.voiceStatus().VoiceStatus.Namespace)
	require.True(t, v.stopListener(true).OK)
	require.False(t, v.voiceStatus().VoiceStatus.Active)
	require.EqualValues(t, 1, starts.Load())
}

func TestVoiceAutostartFailureLeavesDaemonAvailable(t *testing.T) {
	v, _, _ := sherpaRuntime(t, "unused")
	v.sherpaSettings.Set("voice.autostart", true)
	v.sherpaSettings.Set("voice.stream.backend", "sherpa")
	v.sherpaSettings.Set("voice.capture.executable", "/nonexistent/recorder")
	require.Error(t, v.autostartVoice(context.Background()))
	status := v.voiceStatus().VoiceStatus
	require.False(t, status.Active)
	require.Equal(t, "error", status.State)
	require.NotEmpty(t, status.LastError)
	snapshot := v.events.Snapshot()
	require.Equal(t, "error", snapshot.CaptureState)
	require.Equal(t, status.LastError, snapshot.CaptureError)
}
