package daemon

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"

	"github.com/mithrel/ginkgo/internal/ai"
	"github.com/mithrel/ginkgo/internal/db"
	"github.com/mithrel/ginkgo/internal/ipc"
	"github.com/mithrel/ginkgo/internal/wire"
	"github.com/mithrel/ginkgo/pkg/api"
)

func setupVoiceRuntime(t *testing.T, engineBody string) (*voiceRuntime, *wire.App, string) {
	t.Helper()
	dir := t.TempDir()
	wav := make([]byte, 44+320)
	copy(wav, "RIFF")
	binary.LittleEndian.PutUint32(wav[4:], uint32(len(wav)-8))
	copy(wav[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(wav[16:], 16)
	binary.LittleEndian.PutUint16(wav[20:], 1)
	binary.LittleEndian.PutUint16(wav[22:], 1)
	binary.LittleEndian.PutUint32(wav[24:], 16000)
	binary.LittleEndian.PutUint32(wav[28:], 32000)
	binary.LittleEndian.PutUint16(wav[32:], 2)
	binary.LittleEndian.PutUint16(wav[34:], 16)
	copy(wav[36:], "data")
	binary.LittleEndian.PutUint32(wav[40:], 320)
	audio := filepath.Join(dir, "audio.wav")
	model := filepath.Join(dir, "model.bin")
	executable := filepath.Join(dir, "whisper-cli")
	require.NoError(t, os.WriteFile(audio, wav, 0600))
	require.NoError(t, os.WriteFile(model, []byte("lmggfake model"), 0600))
	script := "#!/bin/sh\nset -eu\nout=''\nwhile [ \"$#\" -gt 0 ]; do\n case \"$1\" in\n -of) out=\"$2\"; shift 2 ;;\n *) shift ;;\n esac\ndone\n" + engineBody
	require.NoError(t, os.WriteFile(executable, []byte(script), 0700))
	store, err := db.Open(context.Background(), "sqlite://"+filepath.Join(dir, "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	v := viper.New()
	v.Set("namespace", "work")
	v.Set("default_tags", []string{"Default"})
	v.Set("voice.enabled", true)
	v.Set("voice.stt.executable", executable)
	v.Set("voice.stt.model", model)
	v.Set("voice.stt.device", "cpu")
	app := &wire.App{Cfg: v, Store: store}
	return newVoiceRuntime(app, newAINoteService(app)), app, audio
}

const voiceTestOutput = `printf '%s' '{"result":{"language":"fr"},"transcription":[{"text":"  Bonjour\nmonde!  "}]}' > "$out.json"`

func TestVoiceStreamDurationSettings(t *testing.T) {
	for _, tc := range []struct {
		name       string
		update     string
		window     string
		wantUpdate time.Duration
		wantWindow time.Duration
		wantError  string
	}{
		{name: "unset"},
		{name: "valid", update: "250ms", window: "3s", wantUpdate: 250 * time.Millisecond, wantWindow: 3 * time.Second},
		{name: "invalid update", update: "invalid", window: "3s", wantWindow: 3 * time.Second, wantError: "voice.stream.update_interval must be a positive duration"},
		{name: "zero update", update: "0s", wantError: "voice.stream.update_interval must be a positive duration"},
		{name: "negative window", update: "250ms", window: "-1s", wantUpdate: 250 * time.Millisecond, wantError: "voice.stream.window must be a positive duration"},
		{name: "both invalid", update: "invalid", window: "invalid", wantError: "voice.stream.window must be a positive duration"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, app, _ := setupVoiceRuntime(t, voiceTestOutput)
			if tc.update != "" {
				app.Cfg.Set("voice.stream.update_interval", tc.update)
			}
			if tc.window != "" {
				app.Cfg.Set("voice.stream.window", tc.window)
			}
			runtime := newVoiceRuntime(app, newAINoteService(app))
			require.Equal(t, tc.wantUpdate, runtime.streamUpdate)
			require.Equal(t, tc.wantWindow, runtime.streamWindow)
			if tc.wantError == "" {
				require.NoError(t, runtime.streamErr)
			} else {
				require.EqualError(t, runtime.streamErr, tc.wantError)
			}
		})
	}
}

func TestVoiceRawAndAIDisabledSaveMetadata(t *testing.T) {
	for _, mode := range []string{"raw", "ai"} {
		t.Run(mode, func(t *testing.T) {
			runtime, app, path := setupVoiceRuntime(t, voiceTestOutput)
			r := runtime.transcribe(context.Background(), ipc.Message{AudioPath: path, Save: true, Mode: mode, Namespace: "work"})
			require.True(t, r.OK, r.Msg)
			require.NotNil(t, r.Entry)
			require.Equal(t, "  Bonjour\nmonde!  ", r.Transcript.Text)
			require.Equal(t, r.Transcript.Text, r.Entry.Body)
			require.Equal(t, "Bonjour", r.Entry.Title)
			require.Equal(t, []string{"default"}, r.Entry.Tags)
			require.Equal(t, "create_note", r.Action)
			if mode == "ai" {
				require.Contains(t, r.FallbackReason, "disabled")
			}
			require.Len(t, r.Entry.Provenance.Sources, 1)
			source := r.Entry.Provenance.Sources[0]
			require.Equal(t, "voice", source.Kind)
			require.Equal(t, r.Transcript.Text, source.OriginalText)
			require.Equal(t, "fr", source.Language)
			require.Equal(t, "whisper.cpp", source.STTEngine)
			require.Equal(t, app.Cfg.GetString("voice.stt.model"), source.STTModel)
			require.NotEmpty(t, source.ID)
			require.False(t, source.CapturedAt.IsZero())
		})
	}
}

func TestVoiceTranscriptOnlyAndNoSpeech(t *testing.T) {
	for _, tc := range []struct {
		body string
		save bool
	}{
		{voiceTestOutput, false},
		{`printf '%s' '{"transcription":[]}' > "$out.json"`, true},
		{`printf '%s' '{"transcription":[{"text":" \n "}]}' > "$out.json"`, true},
	} {
		runtime, app, path := setupVoiceRuntime(t, tc.body)
		r := runtime.transcribe(context.Background(), ipc.Message{AudioPath: path, Save: tc.save, Mode: "raw", Namespace: "work"})
		require.True(t, r.OK, r.Msg)
		require.NotNil(t, r.Transcript)
		require.Nil(t, r.Entry)
		entries, _, err := app.Store.Entries.ListEntries(context.Background(), api.ListQuery{})
		require.NoError(t, err)
		require.Empty(t, entries)
	}
}

func TestVoiceDisabledUnsupportedAndCancelled(t *testing.T) {
	runtime, app, path := setupVoiceRuntime(t, "sleep 10\n"+voiceTestOutput)
	app.Cfg.Set("voice.enabled", false)
	app.Cfg.Set("voice.stt.timeout", "invalid")
	disabled := newVoiceRuntime(app, newAINoteService(app))
	r := disabled.transcribe(context.Background(), ipc.Message{AudioPath: "/missing", Save: true})
	require.False(t, r.OK)
	require.Contains(t, r.Msg, "disabled")
	app.Cfg.Set("voice.enabled", true)
	app.Cfg.Set("voice.stt.timeout", "5m")
	app.Cfg.Set("voice.stt.residency", "persistent")
	r = newVoiceRuntime(app, newAINoteService(app)).transcribe(context.Background(), ipc.Message{AudioPath: "/missing"})
	require.False(t, r.OK)
	require.Contains(t, r.Msg, "only on_demand")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	r = runtime.transcribe(ctx, ipc.Message{AudioPath: path, Save: true, Mode: "ai", Namespace: "work"})
	require.False(t, r.OK)
	require.Contains(t, r.Msg, "deadline exceeded")
	entries, _, err := app.Store.Entries.ListEntries(context.Background(), api.ListQuery{})
	require.NoError(t, err)
	require.Empty(t, entries)
}

func TestVoiceAIUnavailablePreservesMetadata(t *testing.T) {
	runtime, app, path := setupVoiceRuntime(t, voiceTestOutput)
	runtime.ai = ai.NewService(app.Store.Entries, nil, ai.ServiceOptions{Enabled: true, DefaultNamespace: "work"})
	r := runtime.transcribe(context.Background(), ipc.Message{AudioPath: path, Save: true, Mode: "ai", Namespace: "work"})
	require.True(t, r.OK, r.Msg)
	require.NotNil(t, r.Entry)
	require.Contains(t, r.FallbackReason, "unavailable")
	require.Equal(t, "voice", r.Entry.Provenance.Sources[0].Kind)
}
