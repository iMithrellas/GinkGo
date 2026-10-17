package cli

import (
	"bytes"
	"testing"

	"github.com/mithrel/ginkgo/internal/ipc"
	"github.com/stretchr/testify/require"
)

func TestVoiceStreamCLIExplicitAndConfiguredTrigger(t *testing.T) {
	for _, save := range []bool{false, true} {
		t.Run(map[bool]string{false: "captions", true: "gated-save"}[save], func(t *testing.T) {
			ctx, messages := startAICommandFake(t, ipc.Response{OK: true, VoiceStatus: &ipc.VoiceStatus{Active: true}})
			cmd := newVoiceCmd()
			cmd.SetContext(ctx)
			getApp(cmd).Cfg.Set("voice.trigger.phrase", "configured phrase")
			getApp(cmd).Cfg.Set("voice.mode", "ai")
			var out bytes.Buffer
			cmd.SetOut(&out)
			args := []string{"stream", "--output", "json"}
			if save {
				args = append(args, "--save", "--trigger", "take note")
			}
			cmd.SetArgs(args)
			require.NoError(t, cmd.Execute())
			m := <-messages
			require.Equal(t, "voice.stream", m.Name)
			require.Equal(t, save, m.Save)
			require.Equal(t, "raw", m.Mode)
			require.Empty(t, m.Action)
			require.True(t, m.Deadline.IsZero())
			if save {
				require.Equal(t, "take note", m.Trigger)
			} else {
				require.Equal(t, "configured phrase", m.Trigger)
			}
			require.Contains(t, out.String(), "voice_status")
		})
	}
}

func TestVoiceStreamRejectsMissingTriggerAndUnsupportedOptions(t *testing.T) {
	for _, args := range [][]string{{"--save"}, {"--output", "yaml"}, {"--trigger", "!!!"}, {"--mode", "ai"}, {"--new"}} {
		ctx, messages := startAICommandFake(t, ipc.Response{})
		cmd := newVoiceCmd()
		cmd.SetContext(ctx)
		cmd.SetArgs(append([]string{"stream"}, args...))
		require.Error(t, cmd.Execute())
		select {
		case <-messages:
			t.Fatal("invalid stream request reached daemon")
		default:
		}
	}
}

func TestVoiceStreamSherpaCompiledLabels(t *testing.T) {
	for _, label := range []string{"", "GINKGO", "OTHER_LABEL"} {
		t.Run("label="+label, func(t *testing.T) {
			ctx, messages := startAICommandFake(t, ipc.Response{OK: true, VoiceStatus: &ipc.VoiceStatus{Active: true}})
			cmd := newVoiceCmd()
			cmd.SetContext(ctx)
			getApp(cmd).Cfg.Set("voice.stream.backend", "sherpa")
			getApp(cmd).Cfg.Set("voice.sherpa.keyword", label)
			getApp(cmd).Cfg.Set("voice.trigger.phrase", "legacy arbitrary phrase")
			cmd.SetArgs([]string{"stream", "--save", "--output", "json"})
			require.NoError(t, cmd.Execute())
			m := <-messages
			require.True(t, m.Save)
			require.Equal(t, label, m.Trigger)
			require.Equal(t, "raw", m.Mode)
		})
	}
	ctx, messages := startAICommandFake(t, ipc.Response{})
	cmd := newVoiceCmd()
	cmd.SetContext(ctx)
	getApp(cmd).Cfg.Set("voice.stream.backend", "sherpa")
	cmd.SetArgs([]string{"stream", "--save", "--trigger", "take arbitrary note"})
	require.ErrorContains(t, cmd.Execute(), "compiled keyword label")
	select {
	case <-messages:
		t.Fatal("invalid phrase sent to daemon")
	default:
	}
}

func TestVoiceStreamSherpaEmptyTriggerHelpAndRequest(t *testing.T) {
	ctx, messages := startAICommandFake(t, ipc.Response{OK: true, VoiceStatus: &ipc.VoiceStatus{Active: true}})
	cmd := newVoiceCmd()
	cmd.SetContext(ctx)
	getApp(cmd).Cfg.Set("voice.stream.backend", "sherpa")
	getApp(cmd).Cfg.Set("voice.sherpa.keyword", "GINKGO")
	cmd.SetArgs([]string{"stream", "--trigger", "", "--output", "json"})
	require.NoError(t, cmd.Execute())
	m := <-messages
	require.Empty(t, m.Trigger, "the daemon resolves an explicit empty selector from its trusted configuration")
	help := newVoiceStreamCmd().Flags().Lookup("trigger").Usage
	require.Contains(t, help, "empty uses configuration; empty configured label selects all")
	require.NotContains(t, help, "empty=all")
}

func TestVoiceStreamTextDistinguishesNoteCaptureFromCaptions(t *testing.T) {
	for _, save := range []bool{false, true} {
		ctx, messages := startAICommandFake(t, ipc.Response{OK: true, VoiceStatus: &ipc.VoiceStatus{Active: true}})
		cmd := newVoiceCmd()
		cmd.SetContext(ctx)
		getApp(cmd).Cfg.Set("voice.stream.backend", "sherpa")
		var out bytes.Buffer
		cmd.SetOut(&out)
		args := []string{"stream"}
		if save {
			args = append(args, "--save")
		}
		cmd.SetArgs(args)
		require.NoError(t, cmd.Execute())
		require.Equal(t, save, (<-messages).Save)
		if save {
			require.Contains(t, out.String(), "Wake-gated raw note capture requested")
		} else {
			require.Contains(t, out.String(), "no notes will be saved")
		}
		require.Contains(t, out.String(), "inspect readiness")
	}
}
