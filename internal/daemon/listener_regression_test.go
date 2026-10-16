package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mithrel/ginkgo/internal/ai"
	"github.com/mithrel/ginkgo/internal/ipc"
	"github.com/mithrel/ginkgo/internal/voice"
	"github.com/mithrel/ginkgo/pkg/api"
)

type listenAIProvider func(context.Context, ai.Request) (ai.Response, error)

func (f listenAIProvider) Complete(ctx context.Context, request ai.Request) (ai.Response, error) {
	return f(ctx, request)
}

func TestListenSaveWithRealTranscriber(t *testing.T) {
	for _, mode := range []string{"raw", "ai"} {
		t.Run(mode, func(t *testing.T) {
			v, app, audio := setupVoiceRuntime(t, voiceTestOutput)
			t.Cleanup(v.close)
			wav, err := os.ReadFile(audio)
			require.NoError(t, err)
			v.capturer = fakeSegmentCapturer(func(ctx context.Context, o voice.CaptureOptions, stop <-chan struct{}, emit func(voice.Segment) error) error {
				return emit(voice.Segment{WAV: wav, Reason: "silence"})
			})
			if mode == "ai" {
				v.ai = ai.NewService(app.Store.Entries, func(string) (ai.Provider, error) {
					return listenAIProvider(func(context.Context, ai.Request) (ai.Response, error) {
						return ai.Response{Text: `{"action":"create_note","title":"AI title","body":"AI body","tags":["voice"]}`}, nil
					}), nil
				}, ai.ServiceOptions{Enabled: true, DefaultNamespace: "work"})
			}
			m := ipc.Message{Save: true, Mode: mode, Namespace: "work"}
			if mode == "ai" {
				m.Action = "create"
			} // CLI --new
			r := v.listen(context.Background(), m, false)
			require.True(t, r.OK, r.Msg)
			require.NotNil(t, r.Transcript)
			require.NotNil(t, r.Entry)
			require.Equal(t, "  Bonjour\nmonde!  ", r.Transcript.Text)
			require.Equal(t, "create_note", r.Action)
			require.Equal(t, r.Entry.ID, r.VoiceStatus.LastNoteID)
			require.Len(t, r.Entry.Provenance.Sources, 1)
			require.Equal(t, r.Transcript.Text, r.Entry.Provenance.Sources[0].OriginalText)
			stored, err := app.Store.Entries.GetEntry(context.Background(), r.Entry.ID)
			require.NoError(t, err)
			if mode == "ai" {
				require.Empty(t, r.FallbackReason)
				require.Equal(t, "AI body", stored.Body)
			} else {
				require.Equal(t, r.Transcript.Text, stored.Body)
			}
		})
	}
}

func TestCanceledListenSaveNeverCommits(t *testing.T) {
	for _, mode := range []string{"raw", "ai"} {
		t.Run(mode, func(t *testing.T) {
			marker := filepath.Join(t.TempDir(), "engine-started")
			v, app, audio := setupVoiceRuntime(t, fmt.Sprintf("printf started > %q\nsleep 10\n", marker)+voiceTestOutput)
			t.Cleanup(v.close)
			wav, err := os.ReadFile(audio)
			require.NoError(t, err)
			v.capturer = fakeSegmentCapturer(func(ctx context.Context, o voice.CaptureOptions, stop <-chan struct{}, emit func(voice.Segment) error) error {
				return emit(voice.Segment{WAV: wav})
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan struct{})
			var r ipc.Response
			go func() {
				m := ipc.Message{Save: true, Mode: mode, Namespace: "work"}
				if mode == "ai" {
					m.Action = "create"
				}
				r = v.listen(ctx, m, false)
				close(done)
			}()
			require.Eventually(t, func() bool { _, err := os.Stat(marker); return err == nil }, 3*time.Second, time.Millisecond)
			cancel()
			awaitListener(t, done)
			require.False(t, r.OK)
			require.Nil(t, r.Entry)
			entries, _, err := app.Store.Entries.ListEntries(context.Background(), api.ListQuery{})
			require.NoError(t, err)
			require.Empty(t, entries)
		})
	}
}

func TestStopBeforeSpeechReturnsEmptyTranscript(t *testing.T) {
	v := listenerRuntime(t)
	started := make(chan struct{})
	v.capturer = fakeSegmentCapturer(func(ctx context.Context, o voice.CaptureOptions, stop <-chan struct{}, emit func(voice.Segment) error) error {
		close(started)
		select {
		case <-stop:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	done := make(chan struct{})
	var r ipc.Response
	go func() { r = v.listen(context.Background(), ipc.Message{Save: true}, false); close(done) }()
	awaitListener(t, started)
	require.True(t, v.stopListener(true).OK)
	awaitListener(t, done)
	require.True(t, r.OK, r.Msg)
	require.NotNil(t, r.Transcript)
	require.Empty(t, r.Transcript.Text)
	require.Nil(t, r.Entry)
	require.Zero(t, r.VoiceStatus.Segments)
}

func TestCancelListenDuringAILeavesNoFallbackNote(t *testing.T) {
	v, app, audio := setupVoiceRuntime(t, voiceTestOutput)
	t.Cleanup(v.close)
	wav, err := os.ReadFile(audio)
	require.NoError(t, err)
	v.capturer = fakeSegmentCapturer(func(ctx context.Context, o voice.CaptureOptions, stop <-chan struct{}, emit func(voice.Segment) error) error {
		return emit(voice.Segment{WAV: wav})
	})
	generating := make(chan struct{})
	v.ai = ai.NewService(app.Store.Entries, func(string) (ai.Provider, error) {
		return listenAIProvider(func(ctx context.Context, request ai.Request) (ai.Response, error) {
			close(generating)
			<-ctx.Done()
			return ai.Response{}, ai.ErrProviderUnavailable
		}), nil
	}, ai.ServiceOptions{Enabled: true, DefaultNamespace: "work"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	var r ipc.Response
	go func() {
		r = v.listen(ctx, ipc.Message{Save: true, Mode: "ai", Action: "create", Namespace: "work"}, false)
		close(done)
	}()
	awaitListener(t, generating)
	cancel()
	awaitListener(t, done)
	require.False(t, r.OK)
	require.Nil(t, r.Entry)
	entries, _, err := app.Store.Entries.ListEntries(context.Background(), api.ListQuery{})
	require.NoError(t, err)
	require.Empty(t, entries)
}

func TestStopFullQueueDrainsOrCanBeCanceled(t *testing.T) {
	for _, reason := range []string{"stop", ""} {
		for _, cancelDrain := range []bool{false, true} {
			t.Run(fmt.Sprintf("reason=%s/cancel=%v", reason, cancelDrain), func(t *testing.T) {
				v := listenerRuntime(t)
				processing, full, flushing := make(chan struct{}), make(chan struct{}), make(chan struct{})
				flushed, release := make(chan struct{}), make(chan struct{})
				var processed atomic.Int64
				v.capturer = fakeSegmentCapturer(func(ctx context.Context, o voice.CaptureOptions, stop <-chan struct{}, emit func(voice.Segment) error) error {
					if err := emit(voice.Segment{WAV: []byte("first")}); err != nil {
						return err
					}
					<-processing
					for i := 0; i < captureQueueSize; i++ {
						if err := emit(voice.Segment{WAV: []byte("queued")}); err != nil {
							return err
						}
					}
					close(full)
					select {
					case <-stop:
					case <-ctx.Done():
						return ctx.Err()
					}
					close(flushing)
					err := emit(voice.Segment{WAV: []byte("final"), Reason: reason})
					close(flushed)
					return err
				})
				v.processSegment = func(ctx context.Context, m ipc.Message, s voice.Segment) ipc.Response {
					if processed.Add(1) == 1 {
						close(processing)
						select {
						case <-release:
						case <-ctx.Done():
							return ipc.Response{Msg: ctx.Err().Error()}
						}
					}
					return ipc.Response{OK: true, Entry: &api.Entry{ID: string(s.WAV)}}
				}
				require.True(t, v.listen(context.Background(), ipc.Message{}, true).OK)
				awaitListener(t, full)
				done := make(chan struct{})
				var stopped ipc.Response
				go func() { stopped = v.stopListener(true); close(done) }()
				awaitListener(t, flushing)
				select {
				case <-done:
					t.Fatal("full queue drain returned before release")
				default:
				}
				if cancelDrain {
					require.True(t, v.stopListener(false).OK)
				} else {
					close(release)
				}
				awaitListener(t, done)
				awaitListener(t, flushed)
				if cancelDrain {
					require.False(t, stopped.OK)
					require.EqualValues(t, 1, processed.Load())
					require.Zero(t, stopped.VoiceStatus.Segments)
				} else {
					require.True(t, stopped.OK, stopped.Msg)
					require.EqualValues(t, captureQueueSize+2, processed.Load())
					require.EqualValues(t, captureQueueSize+2, stopped.VoiceStatus.Segments)
					require.Equal(t, "final", stopped.VoiceStatus.LastNoteID)
				}
				require.NotContains(t, stopped.Msg, "overload")
			})
		}
	}
}

func TestBlockedSyncDoesNotBlockCancelAndShutdownJoinsSync(t *testing.T) {
	v := listenerRuntime(t)
	root, cancel := context.WithCancel(context.Background())
	defer cancel()
	v.root = root
	syncing, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	processed, allProcessed := make(chan struct{}, 1), make(chan struct{})
	defer close(release)
	var calls atomic.Int64
	v.syncNote = func(ctx context.Context) {
		calls.Add(1)
		close(syncing)
		<-ctx.Done()
		close(canceled)
		<-release
	}
	v.capturer = fakeSegmentCapturer(func(ctx context.Context, o voice.CaptureOptions, stop <-chan struct{}, emit func(voice.Segment) error) error {
		if err := emit(voice.Segment{}); err != nil {
			return err
		}
		<-syncing
		<-processed
		for i := 0; i < captureQueueSize+2; i++ {
			if err := emit(voice.Segment{}); err != nil {
				return err
			}
			<-processed
		}
		close(allProcessed)
		<-ctx.Done()
		return ctx.Err()
	})
	v.processSegment = func(context.Context, ipc.Message, voice.Segment) ipc.Response {
		processed <- struct{}{}
		return ipc.Response{OK: true, Entry: &api.Entry{ID: "committed"}}
	}
	require.True(t, v.listen(context.Background(), ipc.Message{}, true).OK)
	awaitListener(t, syncing)
	awaitListener(t, allProcessed) // blocked sync must not consume processing capacity
	for i := 0; i < 100; i++ {
		v.scheduleSync()
	}
	v.mu.Lock()
	pending := len(v.syncPending)
	v.mu.Unlock()
	require.Equal(t, 1, pending)
	done := make(chan struct{})
	go func() { v.stopListener(false); close(done) }()
	awaitListener(t, done)
	select {
	case <-canceled:
		t.Fatal("job cancellation canceled daemon-owned sync")
	default:
	}
	shutdown := make(chan struct{})
	go func() { cancel(); v.close(); close(shutdown) }()
	awaitListener(t, canceled)
	select {
	case <-shutdown:
		t.Fatal("daemon shutdown did not join sync cleanup")
	default:
	}
	release <- struct{}{}
	awaitListener(t, shutdown)
	require.EqualValues(t, 1, calls.Load())
}
