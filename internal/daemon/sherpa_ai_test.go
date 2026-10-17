package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mithrel/ginkgo/internal/ai"
	"github.com/mithrel/ginkgo/internal/ipc"
	"github.com/mithrel/ginkgo/internal/voiceevents"
	"github.com/mithrel/ginkgo/internal/voiceworker"
	"github.com/mithrel/ginkgo/internal/wire"
	"github.com/mithrel/ginkgo/pkg/api"
)

const sherpaAIDraft = `{"action":"create_note","title":"Clean title","body":"Clean body","tags":["generated"]}`

func sherpaAIApp(t *testing.T, v *voiceRuntime) *wire.App {
	t.Helper()
	other, app, _ := setupVoiceRuntime(t, "")
	v.notes = other.notes
	t.Cleanup(other.close)
	return app
}

func TestSherpaOptionalAIConfiguration(t *testing.T) {
	for _, tt := range []struct {
		name, configured, requested, provider string
		wantAI                                bool
	}{
		{"configured ai", "ai", "", "", true},
		{"configured raw", "raw", "", "", false},
		{"empty defaults raw", "", "", "", false},
		{"explicit raw", "ai", "raw", "", false},
		{"explicit ai", "raw", "ai", "", true},
		{"provider override", "raw", "ai", "override", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			v, log, _ := sherpaRuntime(t, "normal")
			app := sherpaAIApp(t, v)
			v.sherpaSettings.Set("voice.mode", tt.configured)
			v.sherpaSettings.Set("voice.sherpa.provider", "cpu")
			var calls atomic.Int32
			v.ai = ai.NewService(app.Store.Entries, func(name string) (ai.Provider, error) {
				require.Equal(t, tt.provider, name, "empty override must delegate configured provider resolution")
				return listenAIProvider(func(_ context.Context, request ai.Request) (ai.Response, error) {
					calls.Add(1)
					require.Equal(t, "configured custom system prompt", request.System)
					var prompt struct{ Input, Action, InputKind string }
					var fields map[string]json.RawMessage
					require.NoError(t, json.Unmarshal([]byte(request.Prompt), &fields))
					require.NoError(t, json.Unmarshal(fields["input"], &prompt.Input))
					require.NoError(t, json.Unmarshal(fields["action"], &prompt.Action))
					require.NoError(t, json.Unmarshal(fields["input_kind"], &prompt.InputKind))
					require.Equal(t, "Ginkgo: Keep Exact CASE!", prompt.Input)
					require.Equal(t, "create", prompt.Action)
					require.Equal(t, "voice", prompt.InputKind)
					return ai.Response{Text: sherpaAIDraft}, nil
				}), nil
			}, ai.ServiceOptions{Enabled: true, DefaultNamespace: "default", DefaultTags: []string{"default"}, SystemPrompt: "configured custom system prompt"})
			r := v.stream(context.Background(), ipc.Message{Save: true, Mode: tt.requested, Provider: tt.provider, Namespace: "work", Title: "Explicit title", Tags: []string{"explicit"}})
			require.True(t, r.OK, r.Msg)
			wake := log.wait(t, func(e voiceevents.Event) bool { return e.RecordingNote })
			require.True(t, v.stopListener(true).OK)
			saved := log.wait(t, func(e voiceevents.Event) bool { return e.Type == "note.saved" })
			require.Equal(t, wake.ActiveNoteSessionID, saved.NoteSessionID)
			selected := log.wait(t, func(e voiceevents.Event) bool { return e.Final && e.NoteSessionID == saved.NoteSessionID })
			require.Less(t, selected.Seq, saved.Seq)
			entries, _, err := app.Store.Entries.ListEntries(context.Background(), api.ListQuery{})
			require.NoError(t, err)
			require.Len(t, entries, 1)
			entry, err := app.Store.Entries.GetEntry(context.Background(), saved.NoteID)
			require.NoError(t, err)
			require.Equal(t, int64(1), entry.Version)
			require.Equal(t, "work", entry.Namespace)
			require.Equal(t, "Explicit title", entry.Title)
			require.Contains(t, entry.Tags, "explicit")
			require.Len(t, entry.Provenance.Sources, 1)
			source := entry.Provenance.Sources[0]
			require.Equal(t, "Ginkgo: Keep Exact CASE!", source.OriginalText)
			require.Equal(t, "voice", source.Kind)
			require.Equal(t, "sherpa-onnx", source.STTEngine)
			require.Equal(t, "parakeet-tdt-0.6b-v2", source.STTModel)
			require.Equal(t, "en", source.Language)
			require.Equal(t, "cpu", v.voiceStatus().VoiceStatus.RequestedProvider)
			require.Empty(t, saved.FallbackReason)
			if tt.wantAI {
				require.EqualValues(t, 1, calls.Load())
				require.Equal(t, "Clean body", entry.Body)
				require.Equal(t, "ai", saved.Processing)
				require.Contains(t, entry.Tags, "default")
				require.Contains(t, entry.Tags, "generated")
			} else {
				require.Zero(t, calls.Load())
				require.Equal(t, "Keep Exact CASE!", entry.Body)
				require.Empty(t, saved.Processing)
			}
		})
	}
}

func TestSherpaAIFallbackIsSingleHonestSave(t *testing.T) {
	for _, tt := range []struct {
		name, draft, reason string
		err                 error
		disabled            bool
	}{
		{name: "disabled", disabled: true, reason: "AI is disabled"},
		{name: "provider failure", err: errors.New("private API key secret /model/path"), reason: "AI provider failed"},
		{name: "invalid draft", draft: "invalid private output", reason: "invalid note draft"},
		{name: "timeout", err: context.DeadlineExceeded, reason: "timed out"},
		{name: "expansion forbidden", draft: `{"action":"expand_note","target_id":"unrelated","title":"No","body":"No","tags":[]}`, reason: "invalid note draft"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			v, log, _ := sherpaRuntime(t, "normal")
			app := sherpaAIApp(t, v)
			var calls atomic.Int32
			v.ai = ai.NewService(app.Store.Entries, func(string) (ai.Provider, error) {
				calls.Add(1)
				return listenAIProvider(func(context.Context, ai.Request) (ai.Response, error) {
					return ai.Response{Text: tt.draft}, tt.err
				}), nil
			}, ai.ServiceOptions{Enabled: !tt.disabled})
			require.True(t, v.stream(context.Background(), ipc.Message{Save: true, Mode: "ai"}).OK)
			log.wait(t, func(e voiceevents.Event) bool { return e.RecordingNote })
			require.True(t, v.stopListener(true).OK)
			saved := log.wait(t, func(e voiceevents.Event) bool { return e.Type == "note.saved" })
			require.Equal(t, "raw_fallback", saved.Processing)
			require.Contains(t, saved.FallbackReason, tt.reason)
			require.NotContains(t, saved.FallbackReason, "private")
			require.NotContains(t, saved.FallbackReason, "secret")
			entries, _, err := app.Store.Entries.ListEntries(context.Background(), api.ListQuery{})
			require.NoError(t, err)
			require.Len(t, entries, 1)
			entry, err := app.Store.Entries.GetEntry(context.Background(), saved.NoteID)
			require.NoError(t, err)
			require.Equal(t, "Ginkgo: Keep Exact CASE!", entry.Body)
			require.Equal(t, "Ginkgo: Keep Exact CASE!", entry.Provenance.Sources[0].OriginalText)
			require.Equal(t, int64(1), entry.Version)
			if tt.disabled {
				require.Zero(t, calls.Load())
			} else {
				require.EqualValues(t, 1, calls.Load())
			}
			for _, e := range log.all() {
				require.NotEqual(t, "note.error", e.Type)
			}
		})
	}
}

func TestSherpaNoSaveNeverCallsAI(t *testing.T) {
	for _, mode := range []string{"", "ai"} {
		t.Run(mode, func(t *testing.T) {
			v, log, _ := sherpaRuntime(t, "normal")
			app := sherpaAIApp(t, v)
			v.sherpaSettings.Set("voice.mode", "ai")
			var calls atomic.Int32
			v.ai = ai.NewService(app.Store.Entries, func(string) (ai.Provider, error) {
				calls.Add(1)
				return nil, errors.New("must not resolve")
			}, ai.ServiceOptions{Enabled: true})
			require.True(t, v.stream(context.Background(), ipc.Message{Mode: mode}).OK)
			log.wait(t, func(e voiceevents.Event) bool { return e.Type == "transcript" })
			require.True(t, v.stopListener(true).OK)
			require.Zero(t, calls.Load())
			entries, _, err := app.Store.Entries.ListEntries(context.Background(), api.ListQuery{})
			require.NoError(t, err)
			require.Empty(t, entries)
		})
	}
}

func TestSherpaAIStopDrainsAndCancelNeverWrites(t *testing.T) {
	for _, finalize := range []bool{true, false} {
		t.Run(map[bool]string{true: "drain", false: "cancel"}[finalize], func(t *testing.T) {
			v, log, _ := sherpaRuntime(t, "unused")
			app := sherpaAIApp(t, v)
			entered, release, stopped := make(chan struct{}), make(chan struct{}), make(chan struct{})
			callback := make(chan func(voiceworker.Event) error, 1)
			v.ai = ai.NewService(app.Store.Entries, func(string) (ai.Provider, error) {
				return listenAIProvider(func(ctx context.Context, _ ai.Request) (ai.Response, error) {
					close(entered)
					select {
					case <-ctx.Done():
						return ai.Response{}, errors.New("private cancellation")
					case <-release:
						return ai.Response{Text: sherpaAIDraft}, nil
					}
				}), nil
			}, ai.ServiceOptions{Enabled: true})
			v.sherpaRunner = fakeWorker(func(ctx context.Context, _ voiceworker.Config, stop <-chan struct{}, emit func(voiceworker.Event) error) error {
				callback <- emit
				if err := emit(voiceworker.Event{Type: "wake", SessionID: "selected", RecordingNote: true}); err != nil {
					return err
				}
				if err := emit(voiceworker.Event{Type: "note_candidate", Candidate: &voiceworker.Candidate{SessionID: "selected", UtteranceID: "note", Body: "body", Original: "Ginkgo body"}}); err != nil {
					return err
				}
				select {
				case <-ctx.Done():
				case <-stop:
				}
				close(stopped)
				return nil
			})
			require.True(t, v.stream(context.Background(), ipc.Message{Save: true, Mode: "ai"}).OK)
			awaitListener(t, entered)
			ack := make(chan ipc.Response, 1)
			go func() { ack <- v.stopListener(finalize) }()
			awaitListener(t, stopped)
			if finalize {
				select {
				case <-ack:
					t.Fatal("stop acknowledged before AI save joined")
				default:
				}
				close(release)
			}
			require.True(t, (<-ack).OK)
			require.Error(t, (<-callback)(voiceworker.Event{Type: "wake", SessionID: "late", RecordingNote: true}))
			entries, _, err := app.Store.Entries.ListEntries(context.Background(), api.ListQuery{})
			require.NoError(t, err)
			if finalize {
				require.Len(t, entries, 1)
				log.wait(t, func(e voiceevents.Event) bool { return e.Type == "note.saved" && e.Processing == "ai" })
			} else {
				require.Empty(t, entries)
				log.wait(t, func(e voiceevents.Event) bool { return e.Type == "note.canceled" })
			}
		})
	}
}

func TestSherpaRejectsUnexpectedModeBeforeExecutableResolution(t *testing.T) {
	for _, mode := range []string{"invalid", "AI"} {
		v, _, _ := sherpaRuntime(t, "unused")
		v.sherpaSettings.Set("voice.capture.executable", "/not-installed")
		v.sherpaSettings.Set("voice.mode", mode)
		require.Equal(t, "voice mode must be raw or ai", v.stream(context.Background(), ipc.Message{}).Msg)
	}
	v, _, _ := sherpaRuntime(t, "unused")
	require.Contains(t, v.stream(context.Background(), ipc.Message{Save: true, Mode: "raw", Provider: "override"}).Msg, "requires save and AI mode")
	require.Contains(t, v.stream(context.Background(), ipc.Message{Mode: "ai", Provider: "override"}).Msg, "requires save and AI mode")
}
