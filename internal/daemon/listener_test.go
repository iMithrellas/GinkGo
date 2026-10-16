package daemon

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mithrel/ginkgo/internal/ipc"
	"github.com/mithrel/ginkgo/internal/notes"
	"github.com/mithrel/ginkgo/internal/voice"
	"github.com/mithrel/ginkgo/pkg/api"
)

type fakeSegmentCapturer func(context.Context, voice.CaptureOptions, <-chan struct{}, func(voice.Segment) error) error

func (f fakeSegmentCapturer) Capture(ctx context.Context, o voice.CaptureOptions, stop <-chan struct{}, emit func(voice.Segment) error) error {
	return f(ctx, o, stop, emit)
}

func listenerRuntime(t *testing.T) *voiceRuntime {
	t.Helper()
	v, _, _ := setupVoiceRuntime(t, voiceTestOutput)
	t.Cleanup(v.close)
	return v
}

func awaitListener(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("listener worker did not finish")
	}
}

func TestListenOnceNoSpeechAndTranscriptOnly(t *testing.T) {
	for _, speech := range []bool{false, true} {
		t.Run(map[bool]string{false: "silence", true: "speech"}[speech], func(t *testing.T) {
			v := listenerRuntime(t)
			v.capturer = fakeSegmentCapturer(func(ctx context.Context, o voice.CaptureOptions, stop <-chan struct{}, emit func(voice.Segment) error) error {
				if !o.Once {
					return errors.New("one-shot capture must set Once")
				}
				if speech {
					return emit(voice.Segment{WAV: []byte("audio")})
				}
				return nil
			})
			var processed bool
			v.processSegment = func(ctx context.Context, m ipc.Message, s voice.Segment) ipc.Response {
				processed = true
				if m.Save {
					return ipc.Response{Msg: "one-shot without save must remain transcript-only"}
				}
				return ipc.Response{OK: true, Transcript: &ipc.Transcript{Text: "private transcript"}}
			}
			r := v.listen(context.Background(), ipc.Message{Namespace: "work"}, false)
			require.True(t, r.OK, r.Msg)
			require.Equal(t, speech, processed)
			require.False(t, r.VoiceStatus.Active)
			require.Equal(t, "idle", r.VoiceStatus.State)
			if speech {
				require.Equal(t, "private transcript", r.Transcript.Text)
			} else {
				require.NotNil(t, r.Transcript)
				require.Empty(t, r.Transcript.Text)
			}
			require.Empty(t, r.VoiceStatus.LastNoteID)
		})
	}
}

func TestContinuousDetachedAndBusy(t *testing.T) {
	v := listenerRuntime(t)
	started := make(chan struct{})
	v.capturer = fakeSegmentCapturer(func(ctx context.Context, o voice.CaptureOptions, stop <-chan struct{}, emit func(voice.Segment) error) error {
		if o.Once {
			return errors.New("continuous stream unexpectedly once")
		}
		close(started)
		<-ctx.Done()
		return ctx.Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	r := v.listen(ctx, ipc.Message{Namespace: "work", Deadline: time.Now().Add(time.Second)}, true)
	require.True(t, r.OK, r.Msg)
	awaitListener(t, started)
	cancel()
	require.True(t, v.voiceStatus().VoiceStatus.Active)
	busy := v.listen(context.Background(), ipc.Message{}, false)
	require.False(t, busy.OK)
	require.Contains(t, busy.Msg, "busy")
	r = v.stopListener(false)
	require.True(t, r.OK, r.Msg)
	require.False(t, r.VoiceStatus.Active)
	require.Equal(t, "canceled", r.VoiceStatus.State)
	require.True(t, v.stopListener(false).OK)
}

func TestGracefulStopFlushesAndDrainsOneStream(t *testing.T) {
	v := listenerRuntime(t)
	processing := make(chan struct{})
	release := make(chan struct{})
	flushed := make(chan struct{})
	var calls, processed, synced atomic.Int64
	v.capturer = fakeSegmentCapturer(func(ctx context.Context, o voice.CaptureOptions, stop <-chan struct{}, emit func(voice.Segment) error) error {
		calls.Add(1)
		if err := emit(voice.Segment{WAV: []byte("first")}); err != nil {
			return err
		}
		<-processing
		select {
		case <-stop:
		case <-ctx.Done():
			return ctx.Err()
		}
		err := emit(voice.Segment{WAV: []byte("flushed")})
		close(flushed)
		return err
	})
	v.processSegment = func(ctx context.Context, m ipc.Message, s voice.Segment) ipc.Response {
		if !m.Save {
			return ipc.Response{Msg: "continuous capture must save"}
		}
		if processed.Add(1) == 1 {
			close(processing)
			<-release
		}
		return ipc.Response{OK: true, Entry: &api.Entry{ID: string(s.WAV)}}
	}
	v.syncNote = func(context.Context) { synced.Add(1) }
	require.True(t, v.listen(context.Background(), ipc.Message{Namespace: "work"}, true).OK)
	awaitListener(t, processing)
	done := make(chan struct{})
	var stopped ipc.Response
	go func() { stopped = v.stopListener(true); close(done) }()
	awaitListener(t, flushed)
	select {
	case <-done:
		t.Fatal("stop returned before pending processing finished")
	default:
	}
	close(release)
	awaitListener(t, done)
	require.True(t, stopped.OK, stopped.Msg)
	require.EqualValues(t, 1, calls.Load())
	require.EqualValues(t, 2, processed.Load())
	require.Eventually(t, func() bool { return synced.Load() >= 1 }, time.Second, time.Millisecond)
	require.LessOrEqual(t, synced.Load(), int64(2)) // adjacent commits can coalesce
	require.EqualValues(t, 2, stopped.VoiceStatus.Segments)
	require.Equal(t, "flushed", stopped.VoiceStatus.LastNoteID)
	require.Equal(t, "idle", stopped.VoiceStatus.State)
}

func TestCancelDiscardsQueuedSegmentsAndJoins(t *testing.T) {
	v := listenerRuntime(t)
	processing := make(chan struct{})
	queued := make(chan struct{})
	release := make(chan struct{})
	canceled := make(chan struct{})
	var count atomic.Int64
	v.capturer = fakeSegmentCapturer(func(ctx context.Context, o voice.CaptureOptions, stop <-chan struct{}, emit func(voice.Segment) error) error {
		if err := emit(voice.Segment{}); err != nil {
			return err
		}
		<-processing
		if err := emit(voice.Segment{}); err != nil {
			return err
		}
		close(queued)
		<-ctx.Done()
		return ctx.Err()
	})
	v.processSegment = func(ctx context.Context, m ipc.Message, s voice.Segment) ipc.Response {
		count.Add(1)
		close(processing)
		<-ctx.Done()
		close(canceled)
		<-release
		return ipc.Response{Msg: ctx.Err().Error()}
	}
	require.True(t, v.listen(context.Background(), ipc.Message{}, true).OK)
	awaitListener(t, queued)
	done := make(chan struct{})
	go func() { v.stopListener(false); close(done) }()
	awaitListener(t, canceled)
	select {
	case <-done:
		t.Fatal("cancel failed to join processing worker")
	default:
	}
	close(release)
	awaitListener(t, done)
	require.EqualValues(t, 1, count.Load())
	require.EqualValues(t, 0, v.voiceStatus().VoiceStatus.Segments)
}

func TestCaptureQueueOverloadExplicitFault(t *testing.T) {
	v := listenerRuntime(t)
	processing := make(chan struct{})
	v.capturer = fakeSegmentCapturer(func(ctx context.Context, o voice.CaptureOptions, stop <-chan struct{}, emit func(voice.Segment) error) error {
		if err := emit(voice.Segment{}); err != nil {
			return err
		}
		<-processing
		for i := 0; i <= captureQueueSize; i++ {
			if err := emit(voice.Segment{}); err != nil {
				// Device cleanup may report cancellation rather than the callback
				// error; the daemon must still retain the overload diagnosis.
				return ctx.Err()
			}
		}
		return errors.New("expected overload")
	})
	v.processSegment = func(ctx context.Context, m ipc.Message, s voice.Segment) ipc.Response {
		close(processing)
		<-ctx.Done()
		return ipc.Response{Msg: ctx.Err().Error()}
	}
	r := v.listen(context.Background(), ipc.Message{}, false)
	require.False(t, r.OK)
	require.Contains(t, r.Msg, "queue overloaded")
	require.Equal(t, "error", r.VoiceStatus.State)
}

func TestOneShotCallerCancellationAndDeadline(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		t.Run(map[bool]string{false: "disconnect", true: "deadline"}[deadline], func(t *testing.T) {
			v := listenerRuntime(t)
			started := make(chan struct{})
			v.capturer = fakeSegmentCapturer(func(ctx context.Context, o voice.CaptureOptions, stop <-chan struct{}, emit func(voice.Segment) error) error {
				close(started)
				<-ctx.Done()
				return ctx.Err()
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			m := ipc.Message{}
			if deadline {
				m.Deadline = time.Now().Add(100 * time.Millisecond)
			}
			finished := make(chan struct{})
			var r ipc.Response
			go func() { r = v.listen(ctx, m, false); close(finished) }()
			awaitListener(t, started)
			if !deadline {
				cancel()
			}
			awaitListener(t, finished)
			require.False(t, r.OK)
			require.Equal(t, "canceled", r.VoiceStatus.State)
			require.False(t, r.VoiceStatus.Active)
		})
	}
}

func TestCloseJoinsDetachedCaptureAndPreventsRestart(t *testing.T) {
	v := listenerRuntime(t)
	started, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	v.capturer = fakeSegmentCapturer(func(ctx context.Context, o voice.CaptureOptions, stop <-chan struct{}, emit func(voice.Segment) error) error {
		close(started)
		<-ctx.Done()
		close(canceled)
		<-release
		return ctx.Err()
	})
	require.True(t, v.listen(context.Background(), ipc.Message{}, true).OK)
	awaitListener(t, started)
	done := make(chan struct{})
	go func() { v.close(); close(done) }()
	awaitListener(t, canceled)
	select {
	case <-done:
		t.Fatal("close failed to join capture")
	default:
	}
	close(release)
	awaitListener(t, done)
	require.False(t, v.listen(context.Background(), ipc.Message{}, true).OK)
}

func TestSimultaneousListenersAdmitOnlyOne(t *testing.T) {
	v := listenerRuntime(t)
	v.capturer = fakeSegmentCapturer(func(ctx context.Context, o voice.CaptureOptions, stop <-chan struct{}, emit func(voice.Segment) error) error {
		<-ctx.Done()
		return ctx.Err()
	})
	var successes atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if v.listen(context.Background(), ipc.Message{}, true).OK {
				successes.Add(1)
			}
		}()
	}
	wg.Wait()
	require.EqualValues(t, 1, successes.Load())
	require.True(t, v.stopListener(false).OK)
}

func TestCaptureValidationBeforeMicrophoneAndSafeStatus(t *testing.T) {
	for _, m := range []ipc.Message{
		{Mode: "unknown"}, {Language: "secret-invalid-language"}, {Action: "expand"}, {Namespace: strings.Repeat("x", 257)},
		{Capture: &ipc.CaptureOptions{SilenceTimeout: -time.Second}}, {Capture: &ipc.CaptureOptions{Threshold: 2}},
		{Capture: &ipc.CaptureOptions{MaxDuration: 149 * time.Millisecond}},
	} {
		v := listenerRuntime(t)
		v.capturer = fakeSegmentCapturer(func(context.Context, voice.CaptureOptions, <-chan struct{}, func(voice.Segment) error) error {
			t.Error("invalid request opened microphone")
			return nil
		})
		r := v.listen(context.Background(), m, true)
		require.False(t, r.OK)
		require.NotContains(t, r.Msg, "secret-invalid-language")
		require.True(t, v.voiceStatus().OK)
		require.True(t, v.stopListener(false).OK)
	}
}

func TestCaptureFaultDoesNotLeakUntrustedError(t *testing.T) {
	v := listenerRuntime(t)
	v.capturer = fakeSegmentCapturer(func(context.Context, voice.CaptureOptions, <-chan struct{}, func(voice.Segment) error) error {
		return errors.New("secret device output and transcript")
	})
	r := v.listen(context.Background(), ipc.Message{}, false)
	require.False(t, r.OK)
	require.NotContains(t, r.Msg, "secret")
	require.NotContains(t, r.VoiceStatus.LastError, "secret")
	require.Equal(t, "error", r.VoiceStatus.State)
}

func TestCaptureSnapshotsReusableRecorderBuffer(t *testing.T) {
	v := listenerRuntime(t)
	mutated := make(chan struct{})
	v.capturer = fakeSegmentCapturer(func(ctx context.Context, o voice.CaptureOptions, stop <-chan struct{}, emit func(voice.Segment) error) error {
		buffer := []byte("original")
		if err := emit(voice.Segment{WAV: buffer}); err != nil {
			return err
		}
		copy(buffer, "modified")
		close(mutated)
		return nil
	})
	v.processSegment = func(ctx context.Context, m ipc.Message, s voice.Segment) ipc.Response {
		<-mutated
		return ipc.Response{OK: true, Transcript: &ipc.Transcript{Text: string(s.WAV)}}
	}
	r := v.listen(context.Background(), ipc.Message{}, false)
	require.True(t, r.OK, r.Msg)
	require.Equal(t, "original", r.Transcript.Text)
}

func TestCaptureBadConfigAndDisabledStillAllowStatusAndCancel(t *testing.T) {
	v, app, _ := setupVoiceRuntime(t, voiceTestOutput)
	app.Cfg.Set("voice.capture.silence", "secret-invalid-duration")
	app.Cfg.Set("voice.enabled", false)
	v = newVoiceRuntime(app, newAINoteService(app))
	r := v.listen(context.Background(), ipc.Message{}, true)
	require.False(t, r.OK)
	require.Contains(t, r.Msg, "disabled")
	require.True(t, v.voiceStatus().OK)
	require.False(t, v.voiceStatus().VoiceStatus.Enabled)
	require.True(t, v.stopListener(false).OK)
	app.Cfg.Set("voice.enabled", true)
	v = newVoiceRuntime(app, newAINoteService(app))
	r = v.listen(context.Background(), ipc.Message{}, true)
	require.False(t, r.OK)
	require.NotContains(t, r.Msg, "secret")
	require.True(t, v.voiceStatus().OK)
	require.True(t, v.stopListener(false).OK)
}

func TestPrivateCaptureWAVCleanedAfterSuccessAndFailure(t *testing.T) {
	for _, body := range []string{voiceTestOutput, "exit 1"} {
		v, _, audio := setupVoiceRuntime(t, body)
		wav, err := os.ReadFile(audio)
		require.NoError(t, err)
		private := t.TempDir()
		t.Setenv("TMPDIR", private)
		v.transcribeSegment(context.Background(), ipc.Message{}, voice.Segment{WAV: wav})
		files, err := os.ReadDir(private)
		require.NoError(t, err)
		require.Empty(t, files)
	}
}

func TestCaptureDefaultsOverridesAndDaemonLifetime(t *testing.T) {
	v := listenerRuntime(t)
	o, err := v.listenOptions(ipc.Message{}, true)
	require.NoError(t, err)
	require.False(t, o.Once)
	require.Equal(t, "pw-record", v.captureConfig.Executable)
	require.Empty(t, o.Target)
	require.Equal(t, 1500*time.Millisecond, o.SilenceTimeout)
	require.Equal(t, 2*time.Minute, o.MaxDuration)
	require.Equal(t, 30*time.Second, o.StartTimeout)
	require.Equal(t, .02, o.Threshold)
	o, err = v.listenOptions(ipc.Message{Capture: &ipc.CaptureOptions{Target: "input", SilenceTimeout: time.Second, MaxDuration: time.Minute, StartTimeout: 2 * time.Second, Threshold: .1}}, false)
	require.NoError(t, err)
	require.True(t, o.Once)
	require.Equal(t, "input", o.Target)
	require.Equal(t, time.Second, o.SilenceTimeout)
	require.Equal(t, time.Minute, o.MaxDuration)
	require.Equal(t, 2*time.Second, o.StartTimeout)
	require.Equal(t, .1, o.Threshold)
	_, err = v.listenOptions(ipc.Message{Capture: &ipc.CaptureOptions{MaxDuration: 150 * time.Millisecond}}, false)
	require.NoError(t, err)

	root, cancel := context.WithCancel(context.Background())
	defer cancel()
	v.root = root
	started := make(chan struct{})
	v.capturer = fakeSegmentCapturer(func(ctx context.Context, o voice.CaptureOptions, stop <-chan struct{}, emit func(voice.Segment) error) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	})
	require.True(t, v.listen(context.Background(), ipc.Message{}, true).OK)
	awaitListener(t, started)
	v.mu.Lock()
	done := v.job.done
	v.mu.Unlock()
	cancel()
	awaitListener(t, done)
	require.False(t, v.voiceStatus().VoiceStatus.Active)
}

func TestCancelKeepsCommittedNoteAndDoesNotSavePending(t *testing.T) {
	v, app, _ := setupVoiceRuntime(t, voiceTestOutput)
	t.Cleanup(v.close)
	committed, pending := make(chan struct{}), make(chan struct{})
	var processed, synced atomic.Int64
	v.capturer = fakeSegmentCapturer(func(ctx context.Context, o voice.CaptureOptions, stop <-chan struct{}, emit func(voice.Segment) error) error {
		if err := emit(voice.Segment{}); err != nil {
			return err
		}
		<-committed
		if err := emit(voice.Segment{}); err != nil {
			return err
		}
		<-ctx.Done()
		return ctx.Err()
	})
	v.processSegment = func(ctx context.Context, m ipc.Message, s voice.Segment) ipc.Response {
		if processed.Add(1) == 1 {
			e, err := v.notes.Create(ctx, notes.CreateRequest{Namespace: "work", Title: "saved", Body: "committed"})
			if err != nil {
				return ipc.Response{Msg: "create failed"}
			}
			close(committed)
			return ipc.Response{OK: true, Entry: &e}
		}
		close(pending)
		<-ctx.Done()
		return ipc.Response{Msg: ctx.Err().Error()}
	}
	v.syncNote = func(context.Context) { synced.Add(1) }
	require.True(t, v.listen(context.Background(), ipc.Message{Namespace: "work"}, true).OK)
	awaitListener(t, pending)
	r := v.stopListener(false)
	require.True(t, r.OK)
	require.EqualValues(t, 1, r.VoiceStatus.Segments)
	require.NotEmpty(t, r.VoiceStatus.LastNoteID)
	require.Eventually(t, func() bool { return synced.Load() == 1 }, time.Second, time.Millisecond)
	entries, _, err := app.Store.Entries.ListEntries(context.Background(), api.ListQuery{Namespace: "work", IncludeBody: true})
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "committed", entries[0].Body)
}
