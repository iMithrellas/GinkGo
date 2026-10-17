package daemon

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mithrel/ginkgo/internal/db"
	"github.com/mithrel/ginkgo/internal/ipc"
	"github.com/mithrel/ginkgo/internal/notes"
	"github.com/mithrel/ginkgo/internal/voice"
	"github.com/mithrel/ginkgo/internal/voiceevents"
	"github.com/mithrel/ginkgo/pkg/api"
)

type fakeLiveCapturer func(context.Context, voice.LiveCaptureOptions, <-chan struct{}, func(voice.LiveCaptureEvent) error) error

func (f fakeLiveCapturer) CaptureLive(ctx context.Context, o voice.LiveCaptureOptions, stop <-chan struct{}, emit func(voice.LiveCaptureEvent) error) error {
	return f(ctx, o, stop, emit)
}

type fakeResident struct {
	transcribe func(context.Context, []byte, voice.Options) (voice.Transcript, error)
	closed     atomic.Bool
}

func (r *fakeResident) Transcribe(ctx context.Context, wav []byte, o voice.Options) (voice.Transcript, error) {
	return r.transcribe(ctx, wav, o)
}
func (r *fakeResident) Close() error { r.closed.Store(true); return nil }

type eventLog struct {
	mu    sync.Mutex
	items []voiceevents.Event
}

func (l *eventLog) all() []voiceevents.Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]voiceevents.Event(nil), l.items...)
}
func (l *eventLog) wait(t *testing.T, match func(voiceevents.Event) bool) voiceevents.Event {
	t.Helper()
	var found voiceevents.Event
	require.Eventually(t, func() bool {
		for _, e := range l.all() {
			if match(e) {
				found = e
				return true
			}
		}
		return false
	}, 5*time.Second, time.Millisecond)
	return found
}

func streamTestRuntime(t *testing.T) (*voiceRuntime, *eventLog, <-chan struct{}, func(voice.LiveCaptureEvent) error) {
	t.Helper()
	v := listenerRuntime(t)
	v.streamEnabled = true
	v.residentConfig = voice.ResidentConfig{Config: voice.Config{Enabled: true, Executable: "fake-installed-server"}, AllowLocalHTTP: true}
	hub, err := voiceevents.NewHub(voiceevents.Options{QueueSize: 128})
	require.NoError(t, err)
	v.events = hub
	dir := t.TempDir()
	require.NoError(t, os.Chmod(dir, 0700))
	path := filepath.Join(dir, "voice.sock")
	server, err := voiceevents.Listen(context.Background(), path, hub)
	require.NoError(t, err)
	log := &eventLog{}
	ctx, cancel := context.WithCancel(context.Background())
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		_ = voiceevents.Watch(ctx, path, func(e voiceevents.Event) error {
			log.mu.Lock()
			log.items = append(log.items, e)
			log.mu.Unlock()
			return nil
		})
	}()
	log.wait(t, func(e voiceevents.Event) bool { return e.Type == "state" })
	t.Cleanup(func() {
		v.close()
		cancel()
		awaitListener(t, watchDone)
		require.NoError(t, server.Close())
		hub.Close()
	})
	commands := make(chan voice.LiveCaptureEvent)
	acks := make(chan error)
	captureDone := make(chan struct{})
	v.liveCapturer = fakeLiveCapturer(func(ctx context.Context, _ voice.LiveCaptureOptions, stop <-chan struct{}, emit func(voice.LiveCaptureEvent) error) error {
		defer close(captureDone)
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-stop:
				return nil
			case e := <-commands:
				err := emit(e)
				acks <- err
				if err != nil {
					return err
				}
			}
		}
	})
	return v, log, captureDone, func(e voice.LiveCaptureEvent) error { commands <- e; return <-acks }
}

func liveEvent(kind string, id, revision uint64, text string) voice.LiveCaptureEvent {
	return voice.LiveCaptureEvent{Kind: kind, UtteranceID: id, Revision: revision, WAV: []byte(text), StartSample: 16000, UtteranceStartSample: 16000, EndSample: 24000, Speech: true, Level: .1, Reason: "silence"}
}

func textResident() *fakeResident {
	return &fakeResident{transcribe: func(_ context.Context, wav []byte, _ voice.Options) (voice.Transcript, error) {
		return voice.Transcript{Text: string(wav), Engine: "fake", Model: "installed"}, nil
	}}
}

func TestStreamIPCPartialsBeforeSilenceAndSaveProvenance(t *testing.T) {
	v, log, captureDone, emit := streamTestRuntime(t)
	r := textResident()
	v.residentFactory = func(voice.ResidentConfig) residentSTT { return r }
	initiator, disconnect := context.WithCancel(context.Background())
	require.True(t, v.stream(initiator, ipc.Message{Save: true, Trigger: "take note", Namespace: "work"}).OK)
	disconnect() // session lifetime is daemon-owned, not request-owned
	require.NoError(t, emit(liveEvent("partial", 1, 1, "take note provisional")))
	p := log.wait(t, func(e voiceevents.Event) bool { return e.Type == "transcript" && !e.Final })
	require.Equal(t, "take note provisional", p.Text)
	require.False(t, p.RecordingNote)
	require.NoError(t, emit(liveEvent("final", 1, 2, "ordinary background")))
	log.wait(t, func(e voiceevents.Event) bool { return e.Type == "transcript" && e.Final })
	require.Empty(t, v.voiceStatus().VoiceStatus.LastNoteID)
	require.NoError(t, emit(liveEvent("final", 2, 1, "Take note: Keep Exact CASE!")))
	saved := log.wait(t, func(e voiceevents.Event) bool { return e.Type == "note.saved" })
	entry, err := v.notes.ScopedGet(context.Background(), saved.NoteID, "work")
	require.NoError(t, err)
	require.Equal(t, "Keep Exact CASE!", entry.Body)
	require.Equal(t, "Take note: Keep Exact CASE!", entry.Provenance.Sources[0].OriginalText)
	require.Equal(t, "voice", entry.Provenance.Sources[0].Kind)
	var recordingSeq, finalSeq uint64
	for _, e := range log.all() {
		if e.Type == "state" && e.ActiveNoteSessionID == saved.NoteSessionID {
			recordingSeq = e.Seq
		}
		if e.Type == "transcript" && e.NoteSessionID == saved.NoteSessionID && e.Final {
			finalSeq = e.Seq
		}
	}
	require.NotZero(t, recordingSeq)
	require.Less(t, recordingSeq, finalSeq)
	require.Less(t, finalSeq, saved.Seq)
	require.False(t, saved.RecordingNote)
	require.True(t, v.stopListener(true).OK)
	awaitListener(t, captureDone)
	require.True(t, r.closed.Load())
	log.wait(t, func(e voiceevents.Event) bool { return e.Type == "state" && !e.Listening && e.Seq > saved.Seq })
	var finalCount, savedCount int
	for _, e := range log.all() {
		if e.Type == "transcript" && e.NoteSessionID == saved.NoteSessionID && e.Final {
			finalCount++
		}
		if e.Type == "note.saved" && e.NoteSessionID == saved.NoteSessionID {
			savedCount++
		}
	}
	require.Equal(t, 1, finalCount)
	require.Equal(t, 1, savedCount)
}

func TestStreamPhraseAloneStateBeforeDelayedInference(t *testing.T) {
	v, log, _, emit := streamTestRuntime(t)
	started, release := make(chan struct{}), make(chan struct{})
	r := textResident()
	r.transcribe = func(ctx context.Context, wav []byte, _ voice.Options) (voice.Transcript, error) {
		if string(wav) == "next dictation" {
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
				return voice.Transcript{}, ctx.Err()
			}
		}
		return voice.Transcript{Text: string(wav)}, nil
	}
	v.residentFactory = func(voice.ResidentConfig) residentSTT { return r }
	require.True(t, v.stream(context.Background(), ipc.Message{Save: true, Trigger: "note", Namespace: "work"}).OK)
	require.NoError(t, emit(liveEvent("final", 1, 1, "note")))
	armed := log.wait(t, func(e voiceevents.Event) bool { return e.Type == "state" && e.RecordingNote })
	require.NoError(t, emit(liveEvent("final", 2, 1, "next dictation")))
	awaitListener(t, started)
	require.True(t, v.events.Snapshot().RecordingNote)
	require.Equal(t, armed.ActiveNoteSessionID, v.events.Snapshot().ActiveNoteSessionID)
	close(release)
	saved := log.wait(t, func(e voiceevents.Event) bool { return e.Type == "note.saved" })
	require.Equal(t, armed.ActiveNoteSessionID, saved.NoteSessionID)
	require.False(t, saved.RecordingNote)
	require.True(t, v.stopListener(true).OK)
}

func TestStreamCaptionOnlyAndNoTriggerNeverSave(t *testing.T) {
	for _, save := range []bool{false, true} {
		t.Run(fmtBool(save), func(t *testing.T) {
			v, log, _, emit := streamTestRuntime(t)
			v.residentFactory = func(voice.ResidentConfig) residentSTT { return textResident() }
			require.True(t, v.stream(context.Background(), ipc.Message{Save: save, Trigger: "note", Namespace: "work"}).OK)
			text := "note explicit caption only"
			if save {
				text = "ordinary background"
			}
			require.NoError(t, emit(liveEvent("final", 1, 1, text)))
			log.wait(t, func(e voiceevents.Event) bool { return e.Type == "transcript" && e.Final })
			require.True(t, v.stopListener(true).OK)
			require.Empty(t, v.voiceStatus().VoiceStatus.LastNoteID)
			for _, e := range log.all() {
				require.NotEqual(t, "note.saved", e.Type)
			}
		})
	}
}
func fmtBool(b bool) string {
	if b {
		return "save-without-trigger-match"
	}
	return "caption-only"
}

func TestStreamDeviceLossFinalNeverCommits(t *testing.T) {
	v, log, done, emit := streamTestRuntime(t)
	var calls atomic.Int64
	v.residentFactory = func(voice.ResidentConfig) residentSTT {
		return &fakeResident{transcribe: func(context.Context, []byte, voice.Options) (voice.Transcript, error) {
			calls.Add(1)
			return voice.Transcript{Text: "note must not save"}, nil
		}}
	}
	require.True(t, v.stream(context.Background(), ipc.Message{Save: true, Trigger: "note"}).OK)
	e := liveEvent("final", 1, 1, "audio")
	e.Reason = "device_lost"
	require.Error(t, emit(e))
	awaitListener(t, done)
	log.wait(t, func(e voiceevents.Event) bool { return e.Type == "error" })
	require.Zero(t, calls.Load())
	require.Empty(t, v.voiceStatus().VoiceStatus.LastNoteID)
}

func TestStreamDisabledConsentAndGPUFailBeforeMic(t *testing.T) {
	for _, kind := range []string{"disabled", "consent", "gpu", "saving-without-trigger"} {
		t.Run(kind, func(t *testing.T) {
			v := listenerRuntime(t)
			hub, err := voiceevents.NewHub(voiceevents.Options{})
			require.NoError(t, err)
			defer hub.Close()
			v.events, v.streamEnabled = hub, true
			v.residentConfig.AllowLocalHTTP = true
			v.residentConfig.Executable = "never-probed"
			v.liveCapturer = fakeLiveCapturer(func(context.Context, voice.LiveCaptureOptions, <-chan struct{}, func(voice.LiveCaptureEvent) error) error {
				t.Error("microphone started")
				return errors.New("unexpected")
			})
			v.residentFactory = func(voice.ResidentConfig) residentSTT { t.Error("resident constructed"); return textResident() }
			m := ipc.Message{}
			switch kind {
			case "disabled":
				v.enabled = false
			case "consent":
				v.residentConfig.AllowLocalHTTP = false
			case "gpu":
				v.sttConfig.Device = "gpu"
			case "saving-without-trigger":
				m.Save = true
			}
			require.False(t, v.stream(context.Background(), m).OK)
		})
	}
}

func TestStreamQueueCoalescesAndFailsExplicitly(t *testing.T) {
	hub, err := voiceevents.NewHub(voiceevents.Options{})
	require.NoError(t, err)
	defer hub.Close()
	var canceled bool
	q := &streamQueue{hub: hub, wake: make(chan struct{}, 1), cancel: func() { canceled = true }}
	for revision := uint64(1); revision <= 100; revision++ {
		require.NoError(t, q.enqueue(liveEvent("partial", 1, revision, "window")))
	}
	require.EqualValues(t, 100, q.partial.Revision)
	require.NoError(t, q.enqueue(liveEvent("final", 1, 101, "whole")))
	require.Nil(t, q.partial)
	require.NoError(t, q.enqueue(liveEvent("partial", 1, 102, "stale")))
	require.Nil(t, q.partial)
	for id := uint64(2); id <= captureQueueSize; id++ {
		require.NoError(t, q.enqueue(liveEvent("final", id, 1, "whole")))
	}
	require.ErrorIs(t, q.enqueue(liveEvent("final", 5, 1, "whole")), errCaptureOverload)
	require.True(t, canceled)
	_, ok, done := q.next()
	require.False(t, ok)
	require.True(t, done)
}

func TestStreamSyntheticPCMPreviewBeforeSilence(t *testing.T) {
	v, log, _, _ := streamTestRuntime(t)
	dir := t.TempDir()
	pcm := make([]byte, 32000)
	for i := 0; i < len(pcm); i += 2 {
		binary.LittleEndian.PutUint16(pcm[i:], 12000)
	}
	path := filepath.Join(dir, "synthetic.pcm")
	require.NoError(t, os.WriteFile(path, pcm, 0600))
	executable := filepath.Join(dir, "fake-recorder")
	require.NoError(t, os.WriteFile(executable, []byte(fmt.Sprintf("#!/bin/sh\ncat -- %q\nexec sleep 60\n", path)), 0700))
	c := v.captureConfig
	c.Executable = executable
	v.liveCapturer = voice.NewCapturer(c)
	r := &fakeResident{transcribe: func(_ context.Context, wav []byte, _ voice.Options) (voice.Transcript, error) {
		if len(wav) < 44 || string(wav[:4]) != "RIFF" {
			return voice.Transcript{}, errors.New("expected synthetic PCM WAV")
		}
		return voice.Transcript{Text: "synthetic preview"}, nil
	}}
	v.residentFactory = func(voice.ResidentConfig) residentSTT { return r }
	require.True(t, v.stream(context.Background(), ipc.Message{}).OK)
	partial := log.wait(t, func(e voiceevents.Event) bool { return e.Type == "transcript" && !e.Final })
	require.Equal(t, "synthetic preview", partial.Text)
	require.True(t, v.voiceStatus().VoiceStatus.Active)
	for _, e := range log.all() {
		require.False(t, e.Final, "no silence/final has been supplied")
	}
	log.wait(t, func(e voiceevents.Event) bool {
		return e.Type == "transcript" && !e.Final && e.AudioEndMS != nil && *e.AudioEndMS == 1000
	})
	require.True(t, v.stopListener(true).OK)
	final := log.wait(t, func(e voiceevents.Event) bool { return e.Type == "transcript" && e.Final })
	require.Equal(t, partial.UtteranceID, final.UtteranceID)
	require.Greater(t, final.Revision, partial.Revision)
	require.NotNil(t, partial.AudioStartMS)
	require.EqualValues(t, 0, *partial.AudioStartMS)
	require.EqualValues(t, 1000, *final.AudioEndMS)
}

func TestStreamCancelJoinsAndDiscardsQueuedSaves(t *testing.T) {
	v, log, captureDone, emit := streamTestRuntime(t)
	started, canceled := make(chan struct{}), make(chan struct{})
	var calls atomic.Int64
	r := &fakeResident{transcribe: func(ctx context.Context, _ []byte, _ voice.Options) (voice.Transcript, error) {
		calls.Add(1)
		close(started)
		<-ctx.Done()
		close(canceled)
		return voice.Transcript{}, ctx.Err()
	}}
	v.residentFactory = func(voice.ResidentConfig) residentSTT { return r }
	require.True(t, v.stream(context.Background(), ipc.Message{Save: true, Trigger: "note"}).OK)
	require.NoError(t, emit(liveEvent("partial", 1, 1, "note draft")))
	awaitListener(t, started)
	require.NoError(t, emit(liveEvent("final", 1, 2, "note must not save")))
	require.True(t, v.stopListener(false).OK)
	awaitListener(t, canceled)
	awaitListener(t, captureDone)
	require.True(t, r.closed.Load())
	require.EqualValues(t, 1, calls.Load())
	require.Empty(t, v.voiceStatus().VoiceStatus.LastNoteID)
	for _, e := range log.all() {
		require.NotEqual(t, "note.saved", e.Type)
		require.NotEqual(t, "transcript", e.Type)
	}
}

func TestStreamSubscriptionConnectDisconnectDoesNotControlCapture(t *testing.T) {
	v, _, _, _ := streamTestRuntime(t)
	var models, microphones atomic.Int64
	v.residentFactory = func(voice.ResidentConfig) residentSTT { models.Add(1); return textResident() }
	v.liveCapturer = fakeLiveCapturer(func(ctx context.Context, _ voice.LiveCaptureOptions, stop <-chan struct{}, _ func(voice.LiveCaptureEvent) error) error {
		microphones.Add(1)
		select {
		case <-stop:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	dir := t.TempDir()
	require.NoError(t, os.Chmod(dir, 0700))
	path := filepath.Join(dir, "another.sock")
	s, err := voiceevents.Listen(context.Background(), path, v.events)
	require.NoError(t, err)
	defer s.Close()
	connect := func() {
		conn, err := net.Dial("unix", path)
		require.NoError(t, err)
		buf := make([]byte, 4096)
		_, err = conn.Read(buf)
		require.NoError(t, err)
		require.NoError(t, conn.Close())
	}
	connect()
	require.Zero(t, models.Load())
	require.Zero(t, microphones.Load())
	require.False(t, v.voiceStatus().VoiceStatus.Active)
	require.True(t, v.stream(context.Background(), ipc.Message{}).OK)
	require.Eventually(t, func() bool { return microphones.Load() == 1 }, time.Second, time.Millisecond)
	connect()
	require.True(t, v.voiceStatus().VoiceStatus.Active)
	require.EqualValues(t, 1, models.Load())
	require.EqualValues(t, 1, microphones.Load())
	v.close()
	require.False(t, v.voiceStatus().VoiceStatus.Active)
}

type failingStreamRepo struct{ db.EntryRepo }

func (failingStreamRepo) CreateEntry(context.Context, api.Entry) (api.Entry, error) {
	return api.Entry{}, errors.New("private database diagnostic")
}

func TestStreamNoteErrorSeparateFromFinalAndSanitized(t *testing.T) {
	v, log, _, emit := streamTestRuntime(t)
	v.residentFactory = func(voice.ResidentConfig) residentSTT { return textResident() }
	v.notes = notes.NewService(failingStreamRepo{})
	require.True(t, v.stream(context.Background(), ipc.Message{Save: true, Trigger: "note"}).OK)
	require.NoError(t, emit(liveEvent("final", 1, 1, "note selected content")))
	failed := log.wait(t, func(e voiceevents.Event) bool { return e.Type == "note.error" })
	require.Equal(t, "voice note save failed", failed.Error)
	require.NotEmpty(t, failed.NoteSessionID)
	require.False(t, failed.RecordingNote)
	final := log.wait(t, func(e voiceevents.Event) bool { return e.Type == "transcript" && e.Final })
	require.Equal(t, final.NoteSessionID, failed.NoteSessionID)
	require.Less(t, final.Seq, failed.Seq)
	require.True(t, v.stopListener(true).OK)
}

func TestStreamStopArmedCancelsWithoutEmptyNote(t *testing.T) {
	v, log, _, emit := streamTestRuntime(t)
	v.residentFactory = func(voice.ResidentConfig) residentSTT { return textResident() }
	require.True(t, v.stream(context.Background(), ipc.Message{Save: true, Trigger: "note"}).OK)
	require.NoError(t, emit(liveEvent("final", 1, 1, "note")))
	armed := log.wait(t, func(e voiceevents.Event) bool { return e.Type == "state" && e.RecordingNote })
	require.True(t, v.stopListener(true).OK)
	canceled := log.wait(t, func(e voiceevents.Event) bool { return e.Type == "note.canceled" })
	require.Equal(t, armed.ActiveNoteSessionID, canceled.NoteSessionID)
	require.False(t, canceled.RecordingNote)
	require.False(t, canceled.Listening)
	require.Empty(t, v.voiceStatus().VoiceStatus.LastNoteID)
}

func TestStreamDelayedPartialCannotFollowFinal(t *testing.T) {
	v, log, _, emit := streamTestRuntime(t)
	started, release := make(chan struct{}), make(chan struct{})
	r := textResident()
	r.transcribe = func(ctx context.Context, wav []byte, _ voice.Options) (voice.Transcript, error) {
		if string(wav) == "old preview" {
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
				return voice.Transcript{}, ctx.Err()
			}
		}
		return voice.Transcript{Text: string(wav)}, nil
	}
	v.residentFactory = func(voice.ResidentConfig) residentSTT { return r }
	require.True(t, v.stream(context.Background(), ipc.Message{}).OK)
	require.NoError(t, emit(liveEvent("partial", 1, 1, "old preview")))
	awaitListener(t, started)
	for revision := uint64(2); revision <= 20; revision++ {
		require.NoError(t, emit(liveEvent("partial", 1, revision, "coalesced preview")))
	}
	require.NoError(t, emit(liveEvent("final", 1, 21, "whole final")))
	close(release)
	final := log.wait(t, func(e voiceevents.Event) bool { return e.Type == "transcript" && e.Final })
	require.NoError(t, emit(liveEvent("partial", 1, 22, "stale callback")))
	require.True(t, v.stopListener(true).OK)
	log.wait(t, func(e voiceevents.Event) bool { return e.Type == "state" && !e.Listening && e.Seq > final.Seq })
	var partials int
	for _, e := range log.all() {
		if e.Type == "transcript" && !e.Final {
			partials++
			require.Less(t, e.Seq, final.Seq)
			require.EqualValues(t, 1, e.Revision)
		}
	}
	require.Equal(t, 1, partials)
	require.EqualValues(t, 21, final.Revision)
	require.Equal(t, "whole final", final.Text)
}

func TestStreamOverloadCancelsActiveInferenceAndJoins(t *testing.T) {
	v, log, captureDone, emit := streamTestRuntime(t)
	started := make(chan struct{})
	r := &fakeResident{transcribe: func(ctx context.Context, _ []byte, _ voice.Options) (voice.Transcript, error) {
		close(started)
		<-ctx.Done()
		return voice.Transcript{}, ctx.Err()
	}}
	v.residentFactory = func(voice.ResidentConfig) residentSTT { return r }
	require.True(t, v.stream(context.Background(), ipc.Message{Save: true, Trigger: "note"}).OK)
	require.NoError(t, emit(liveEvent("partial", 1, 1, "preview")))
	awaitListener(t, started)
	for id := uint64(1); id <= captureQueueSize; id++ {
		require.NoError(t, emit(liveEvent("final", id, 2, "note retained until explicit fault")))
	}
	require.ErrorIs(t, emit(liveEvent("final", 5, 2, "overflow")), errCaptureOverload)
	awaitListener(t, captureDone)
	log.wait(t, func(e voiceevents.Event) bool { return e.Type == "error" && e.Error == errCaptureOverload.Error() })
	require.True(t, r.closed.Load())
	require.Eventually(t, func() bool { return !v.voiceStatus().VoiceStatus.Active }, time.Second, time.Millisecond)
	require.Empty(t, v.voiceStatus().VoiceStatus.LastNoteID)
}
