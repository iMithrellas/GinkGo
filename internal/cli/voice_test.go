package cli

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mithrel/ginkgo/internal/ipc"
	"github.com/mithrel/ginkgo/pkg/api"
)

func TestVoiceTranscribeCLIRequest(t *testing.T) {
	ctx, messages := startAICommandFake(t, ipc.Response{OK: true, Transcript: &ipc.Transcript{Text: " exact\ntranscript ", Language: "en", Engine: "whisper.cpp", Model: "model"}, Entry: &api.Entry{ID: "saved", Title: "title"}, Action: "create_note", FallbackReason: "AI is disabled"})
	cmd := newVoiceCmd()
	cmd.SetContext(ctx)
	var out, diagnostics bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&diagnostics)
	cmd.SetArgs([]string{"transcribe", "recording.wav", "--save", "--mode", "ai", "--new", "--provider", "test", "--title", "override", "--tag", "one", "--model", "ggml-base.bin", "--language", "en", "--output", "json"})
	require.NoError(t, cmd.Execute())
	m := <-messages
	path, err := filepath.Abs("recording.wav")
	require.NoError(t, err)
	require.Equal(t, "voice.transcribe", m.Name)
	require.Equal(t, path, m.AudioPath)
	require.True(t, filepath.IsAbs(m.Model))
	require.True(t, m.Save)
	require.Equal(t, "ai", m.Mode)
	require.Equal(t, "create", m.Action)
	require.Equal(t, "test", m.Provider)
	require.Equal(t, "en", m.Language)
	require.Equal(t, "override", m.Title)
	require.Equal(t, []string{"one"}, m.Tags)
	require.WithinDuration(t, time.Now().Add(8*time.Minute), m.Deadline, time.Second)
	var result map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(out.Bytes(), &result))
	require.Contains(t, result, "transcript")
	require.Contains(t, result, "entry")
	require.Contains(t, result, "fallback_reason")
	require.Empty(t, diagnostics.String())
}

func TestVoiceTranscribeCLITranscriptOnly(t *testing.T) {
	ctx, messages := startAICommandFake(t, ipc.Response{OK: true, Transcript: &ipc.Transcript{Text: " exact\ntranscript\n "}})
	cmd := newVoiceCmd()
	cmd.SetContext(ctx)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"transcribe", "recording.wav"})
	require.NoError(t, cmd.Execute())
	m := <-messages
	require.False(t, m.Save)
	require.Equal(t, "raw", m.Mode)
	require.Equal(t, " exact\ntranscript\n ", out.String())
}

func TestVoiceTranscribeCLIRejectsUnsafeActions(t *testing.T) {
	for _, args := range [][]string{
		{"--new"}, {"--note", "target", "--save"}, {"--new", "--mode", "ai"}, {"--mode", "invalid"}, {"--timeout", "0s"},
	} {
		ctx, _ := startAICommandFake(t, ipc.Response{})
		cmd := newVoiceCmd()
		cmd.SetContext(ctx)
		cmd.SetArgs(append([]string{"transcribe", "recording.wav"}, args...))
		require.Error(t, cmd.Execute())
	}
}
