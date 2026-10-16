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

func TestVoiceEnableAndListenCLIControls(t *testing.T) {
	ctx, messages := startAICommandFake(t, ipc.Response{OK: true, VoiceStatus: &ipc.VoiceStatus{Enabled: true, Continuous: true, State: "listening"}})
	cmd := newVoiceCmd()
	cmd.SetContext(ctx)
	cmd.SetArgs([]string{"enable", "--silence", "2s", "--device", "mic"})
	require.NoError(t, cmd.Execute())
	m := <-messages
	require.Equal(t, "voice.enable", m.Name)
	require.True(t, m.Save)
	require.Equal(t, "mic", m.Capture.Target)
	require.Equal(t, 2*time.Second, m.Capture.SilenceTimeout)
}

func TestVoiceControlsValidateExplicitCaptureFlags(t *testing.T) {
	for _, args := range [][]string{{"listen", "--silence", "-1s"}, {"listen", "--silence", "61s"}, {"listen", "--threshold", "2"}, {"listen", "--threshold", "NaN"}, {"listen", "--threshold", "+Inf"}, {"listen", "--max-duration", "0s"}, {"listen", "--max-duration", "100ms"}, {"listen", "--max-duration", "5m1s"}, {"enable", "--max-duration", "100ms"}, {"enable", "--max-duration", "6m"}, {"listen", "--start-timeout", "6m"}} {
		ctx, messages := startAICommandFake(t, ipc.Response{})
		cmd := newVoiceCmd()
		cmd.SetContext(ctx)
		cmd.SetArgs(args)
		require.ErrorContains(t, cmd.Execute(), args[1])
		select {
		case <-messages:
			t.Fatalf("invalid flags reached daemon: %v", args)
		default:
		}
	}
}

func TestVoiceListenUsesConfigAndPrefersSavedEntry(t *testing.T) {
	ctx, messages := startAICommandFake(t, ipc.Response{OK: true, VoiceStatus: &ipc.VoiceStatus{Active: true}, Transcript: &ipc.Transcript{Text: "raw text"}, Entry: &api.Entry{ID: "note-id", Title: "Saved"}, FallbackReason: "provider unavailable"})
	cmd := newVoiceCmd()
	cmd.SetContext(ctx)
	app := getApp(cmd)
	app.Cfg.Set("voice.mode", "ai")
	var out, diagnostics bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&diagnostics)
	cmd.SetArgs([]string{"listen", "--model", "model.bin", "--save"})
	require.NoError(t, cmd.Execute())
	m := <-messages
	require.Equal(t, "ai", m.Mode)
	require.True(t, filepath.IsAbs(m.Model))
	require.Equal(t, "note-id\tSaved\n", out.String())
	require.Contains(t, diagnostics.String(), "Raw fallback: provider unavailable")
}

func TestVoiceControlFlagsAreActionSpecificAndPayloadRequired(t *testing.T) {
	ctx, _ := startAICommandFake(t, ipc.Response{OK: true})
	status := newVoiceCmd()
	status.SetContext(ctx)
	status.SetArgs([]string{"status"})
	var out bytes.Buffer
	status.SetOut(&out)
	require.ErrorContains(t, status.Execute(), "missing voice status")
	jsonStatus := newVoiceCmd()
	jsonStatus.SetContext(ctx)
	jsonStatus.SetArgs([]string{"status", "--output", "json"})
	require.ErrorContains(t, jsonStatus.Execute(), "missing voice status")
	for _, name := range []string{"status", "disable", "stop", "cancel"} {
		cmd := newVoiceCmd()
		child, _, err := cmd.Find([]string{name})
		require.NoError(t, err)
		for _, forbidden := range []string{"save", "silence", "max-duration", "start-timeout", "threshold", "device", "mode", "model", "language"} {
			require.Nil(t, child.Flags().Lookup(forbidden), name+" unexpectedly exposes --"+forbidden)
		}
	}
}

func TestVoiceEnableUsesConfiguredModeAndAbsoluteModel(t *testing.T) {
	ctx, messages := startAICommandFake(t, ipc.Response{OK: true, VoiceStatus: &ipc.VoiceStatus{Enabled: true}})
	cmd := newVoiceCmd()
	cmd.SetContext(ctx)
	getApp(cmd).Cfg.Set("voice.mode", "ai")
	cmd.SetArgs([]string{"enable", "--model", "model.bin", "--new"})
	require.NoError(t, cmd.Execute())
	m := <-messages
	require.Equal(t, "voice.enable", m.Name)
	require.True(t, m.Save)
	require.Equal(t, "ai", m.Mode)
	require.Equal(t, "create", m.Action)
	require.True(t, filepath.IsAbs(m.Model))
	require.NotNil(t, m.Capture)
}

func TestVoiceEnableRejectsAIActionInRawMode(t *testing.T) {
	ctx, messages := startAICommandFake(t, ipc.Response{OK: true})
	cmd := newVoiceCmd()
	cmd.SetContext(ctx)
	cmd.SetArgs([]string{"enable", "--mode", "raw", "--new"})
	require.ErrorContains(t, cmd.Execute(), "require AI mode")
	select {
	case <-messages:
		t.Fatal("invalid note action reached daemon")
	default:
	}
}

func TestVoiceStatusDisplaysCaptureFailure(t *testing.T) {
	ctx, _ := startAICommandFake(t, ipc.Response{OK: true, VoiceStatus: &ipc.VoiceStatus{State: "error", LastError: "voice capture failed"}})
	cmd := newVoiceCmd()
	cmd.SetContext(ctx)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"status"})
	require.NoError(t, cmd.Execute())
	require.Contains(t, out.String(), `last_error="voice capture failed"`)
}
