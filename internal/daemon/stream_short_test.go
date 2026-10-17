package daemon

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mithrel/ginkgo/internal/ipc"
	"github.com/mithrel/ginkgo/internal/voice"
	"github.com/mithrel/ginkgo/internal/voiceevents"
)

type captureAudit struct {
	mu     sync.Mutex
	events []voice.LiveCaptureEvent
}

func (a *captureAudit) all() []voice.LiveCaptureEvent {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]voice.LiveCaptureEvent(nil), a.events...)
}

func (a *captureAudit) wait(t *testing.T, match func(voice.LiveCaptureEvent) bool) {
	t.Helper()
	require.Eventually(t, func() bool {
		for _, e := range a.all() {
			if match(e) {
				return true
			}
		}
		return false
	}, time.Second, time.Millisecond)
}

// Use the real public CaptureLive API and a pipe-fed synthetic recorder. No
// acoustic device, Python helper, model, or installed inference server is used.
func syntheticGateRuntime(t *testing.T) (*voiceRuntime, *eventLog, *captureAudit, <-chan struct{}, func(int, int16) error) {
	t.Helper()
	v, log, _, _ := streamTestRuntime(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "synthetic.pcm")
	require.NoError(t, syscall.Mkfifo(path, 0600))
	input, err := os.OpenFile(path, os.O_RDWR, 0600)
	require.NoError(t, err)
	executable := filepath.Join(dir, "fake-recorder")
	require.NoError(t, os.WriteFile(executable, []byte(fmt.Sprintf("#!/bin/sh\nexec cat -- %q\n", path)), 0700))
	c := v.captureConfig
	c.Executable, c.SilenceTimeout = executable, 20*time.Millisecond
	v.captureConfig = c
	v.streamUpdate = 10 * time.Millisecond
	recorder := voice.NewCapturer(c)
	audit, done := &captureAudit{}, make(chan struct{})
	v.liveCapturer = fakeLiveCapturer(func(ctx context.Context, o voice.LiveCaptureOptions, stop <-chan struct{}, emit func(voice.LiveCaptureEvent) error) error {
		defer close(done)
		return recorder.CaptureLive(ctx, o, stop, func(e voice.LiveCaptureEvent) error {
			err := emit(e)
			observation := e
			observation.WAV = nil
			audit.mu.Lock()
			audit.events = append(audit.events, observation)
			audit.mu.Unlock()
			return err
		})
	})
	t.Cleanup(func() { v.close(); require.NoError(t, input.Close()) })
	feed := func(frames int, sample int16) error {
		pcm := make([]byte, frames*320) // 10ms PCM16/16kHz mono frames
		for offset := 0; offset < len(pcm); offset += 2 {
			binary.LittleEndian.PutUint16(pcm[offset:], uint16(sample))
		}
		_, err := input.Write(pcm)
		return err
	}
	return v, log, audit, done, feed
}

func syntheticTranscript(wav []byte) (voice.Transcript, error) {
	var maximum uint16
	for offset := 44; offset+1 < len(wav); offset += 2 {
		maximum = max(maximum, binary.LittleEndian.Uint16(wav[offset:]))
	}
	switch maximum {
	case 3000:
		return voice.Transcript{Text: "note"}, nil
	case 9000:
		return voice.Transcript{Text: "exact next dictation"}, nil
	default:
		return voice.Transcript{}, errors.New("unqualified noise must not reach inference")
	}
}

func TestStreamDiscardedPulseKeepsArmForQualifyingDictation(t *testing.T) {
	for _, shortFrames := range []int{1, 14} {
		t.Run(fmt.Sprintf("%dms", shortFrames*10), func(t *testing.T) {
			v, log, audit, captureDone, feed := syntheticGateRuntime(t)
			r := &fakeResident{transcribe: func(_ context.Context, wav []byte, _ voice.Options) (voice.Transcript, error) {
				return syntheticTranscript(wav)
			}}
			v.residentFactory = func(voice.ResidentConfig) residentSTT { return r }
			require.True(t, v.stream(context.Background(), ipc.Message{Save: true, Trigger: "note", Namespace: "work"}).OK)
			require.NoError(t, feed(15, 3000))
			require.NoError(t, feed(3, 0))
			armed := log.wait(t, func(e voiceevents.Event) bool { return e.Type == "state" && e.RecordingNote })
			require.NoError(t, feed(shortFrames, 6000))
			require.NoError(t, feed(3, 0))
			end := int64(18+shortFrames+3) * 160
			audit.wait(t, func(e voice.LiveCaptureEvent) bool {
				return e.Kind == "activity" && e.UtteranceID == 0 && e.EndSample >= end
			})
			for _, e := range audit.all() {
				if e.UtteranceID == 2 {
					require.Equal(t, "activity", e.Kind)
				}
			}
			require.True(t, v.events.Snapshot().RecordingNote)
			require.NoError(t, feed(15, 9000))
			require.NoError(t, feed(3, 0))
			saved := log.wait(t, func(e voiceevents.Event) bool { return e.Type == "note.saved" })
			require.Equal(t, armed.ActiveNoteSessionID, saved.NoteSessionID)
			entry, err := v.notes.ScopedGet(context.Background(), saved.NoteID, "work")
			require.NoError(t, err)
			require.Equal(t, "exact next dictation", entry.Body)
			require.True(t, v.stopListener(true).OK)
			awaitListener(t, captureDone)
			require.True(t, r.closed.Load())
			log.wait(t, func(e voiceevents.Event) bool { return e.Type == "state" && !e.Listening && e.Seq > saved.Seq })
			var saves, finals int
			for _, e := range log.all() {
				if e.Type == "note.saved" {
					saves++
				}
				if e.Type == "transcript" && e.Final && e.NoteSessionID == saved.NoteSessionID {
					finals++
				}
			}
			require.Equal(t, 1, saves)
			// Trigger-only final and selected dictation both attribute the same session.
			require.Equal(t, 2, finals)
		})
	}
}

func TestStreamShortPulseExpiryAndStopCancelCleanup(t *testing.T) {
	for _, ending := range []string{"expiry", "stop", "cancel"} {
		t.Run(ending, func(t *testing.T) {
			v, log, audit, captureDone, feed := syntheticGateRuntime(t)
			r := &fakeResident{transcribe: func(_ context.Context, wav []byte, _ voice.Options) (voice.Transcript, error) {
				return syntheticTranscript(wav)
			}}
			v.residentFactory = func(voice.ResidentConfig) residentSTT { return r }
			require.True(t, v.stream(context.Background(), ipc.Message{Save: true, Trigger: "note"}).OK)
			require.NoError(t, feed(15, 3000))
			require.NoError(t, feed(3, 0))
			armed := log.wait(t, func(e voiceevents.Event) bool { return e.Type == "state" && e.RecordingNote })
			require.NoError(t, feed(1, 6000))
			require.NoError(t, feed(3, 0))
			audit.wait(t, func(e voice.LiveCaptureEvent) bool {
				return e.Kind == "activity" && e.UtteranceID == 0 && e.EndSample >= 22*160
			})
			if ending == "expiry" {
				// Keep the synthetic device alive with silence; do not conflate gate
				// expiry with the capture backend's independent five-second stall.
				until := time.NewTimer(7 * time.Second)
				defer until.Stop()
				tick := time.NewTicker(50 * time.Millisecond)
				defer tick.Stop()
				waiting := true
				for waiting {
					select {
					case <-until.C:
						t.Fatal("noise pulse stranded recording state past trigger timeout")
					case <-tick.C:
						require.NoError(t, feed(1, 0))
						waiting = v.events.Snapshot().RecordingNote
					}
				}
				require.True(t, v.events.Snapshot().Listening)
			}
			require.True(t, v.stopListener(ending != "cancel").OK)
			awaitListener(t, captureDone)
			require.True(t, r.closed.Load())
			canceled := log.wait(t, func(e voiceevents.Event) bool { return e.Type == "note.canceled" })
			require.Equal(t, armed.ActiveNoteSessionID, canceled.NoteSessionID)
			require.False(t, canceled.RecordingNote)
			require.Empty(t, v.voiceStatus().VoiceStatus.LastNoteID)
			log.wait(t, func(e voiceevents.Event) bool { return e.Type == "state" && !e.Listening && e.Seq > armed.Seq })
			var cancels int
			for _, e := range log.all() {
				require.NotEqual(t, "note.saved", e.Type)
				if e.Type == "note.canceled" {
					cancels++
				}
			}
			require.Equal(t, 1, cancels)
		})
	}
}

func TestStreamDelayedActivationDoesNotSelectObservedAmbientPulse(t *testing.T) {
	v, log, audit, captureDone, feed := syntheticGateRuntime(t)
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	r := &fakeResident{transcribe: func(ctx context.Context, wav []byte, _ voice.Options) (voice.Transcript, error) {
		first := false
		once.Do(func() { first = true; close(started) })
		if first {
			select {
			case <-release:
			case <-ctx.Done():
				return voice.Transcript{}, ctx.Err()
			}
		}
		return syntheticTranscript(wav)
	}}
	v.residentFactory = func(voice.ResidentConfig) residentSTT { return r }
	require.True(t, v.stream(context.Background(), ipc.Message{Save: true, Trigger: "note"}).OK)
	require.NoError(t, feed(15, 3000))
	require.NoError(t, feed(3, 0))
	awaitListener(t, started)
	require.NoError(t, feed(1, 9000))
	audit.wait(t, func(e voice.LiveCaptureEvent) bool { return e.Kind == "activity" && e.UtteranceID == 2 && e.Speech })
	close(release)
	armed := log.wait(t, func(e voiceevents.Event) bool { return e.Type == "state" && e.RecordingNote })
	require.NoError(t, feed(14, 9000))
	require.NoError(t, feed(3, 0))
	ambient := log.wait(t, func(e voiceevents.Event) bool {
		return e.Type == "transcript" && e.Final && e.Text == "exact next dictation"
	})
	require.Empty(t, ambient.NoteSessionID)
	require.True(t, v.events.Snapshot().RecordingNote)
	require.Empty(t, v.voiceStatus().VoiceStatus.LastNoteID)
	require.NoError(t, feed(15, 9000))
	require.NoError(t, feed(3, 0))
	saved := log.wait(t, func(e voiceevents.Event) bool { return e.Type == "note.saved" })
	require.Equal(t, armed.ActiveNoteSessionID, saved.NoteSessionID)
	require.True(t, v.stopListener(true).OK)
	awaitListener(t, captureDone)
	require.True(t, r.closed.Load())
}

func TestStreamQualifyingSpeechSurvivesInitialWaitDeadline(t *testing.T) {
	v, log, _, captureDone, feed := syntheticGateRuntime(t)
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	r := &fakeResident{transcribe: func(ctx context.Context, wav []byte, _ voice.Options) (voice.Transcript, error) {
		tr, err := syntheticTranscript(wav)
		if tr.Text == "exact next dictation" {
			first := false
			once.Do(func() { first = true; close(started) })
			if first {
				select {
				case <-release:
				case <-ctx.Done():
					return voice.Transcript{}, ctx.Err()
				}
			}
		}
		return tr, err
	}}
	v.residentFactory = func(voice.ResidentConfig) residentSTT { return r }
	require.True(t, v.stream(context.Background(), ipc.Message{Save: true, Trigger: "note", Namespace: "work"}).OK)
	require.NoError(t, feed(15, 3000))
	require.NoError(t, feed(3, 0))
	armed := log.wait(t, func(e voiceevents.Event) bool { return e.Type == "state" && e.RecordingNote })
	require.NoError(t, feed(15, 9000))
	awaitListener(t, started)
	// Keep qualified speech in progress while both the initial gate deadline and
	// inference latency elapse. Capture must remain live and coalesce previews.
	unblock := time.NewTimer(5200 * time.Millisecond)
	defer unblock.Stop()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	waiting := true
	for waiting {
		select {
		case <-unblock.C:
			waiting = false
		case <-tick.C:
			require.True(t, v.events.Snapshot().RecordingNote)
			require.NoError(t, feed(1, 9000))
		}
	}
	require.Equal(t, armed.ActiveNoteSessionID, v.events.Snapshot().ActiveNoteSessionID)
	close(release)
	require.NoError(t, feed(3, 0))
	saved := log.wait(t, func(e voiceevents.Event) bool { return e.Type == "note.saved" })
	require.Equal(t, armed.ActiveNoteSessionID, saved.NoteSessionID)
	require.True(t, v.stopListener(true).OK)
	awaitListener(t, captureDone)
	require.True(t, r.closed.Load())
	log.wait(t, func(e voiceevents.Event) bool { return e.Type == "state" && !e.Listening && e.Seq > saved.Seq })
	for _, e := range log.all() {
		require.NotEqual(t, "note.canceled", e.Type)
	}
}
