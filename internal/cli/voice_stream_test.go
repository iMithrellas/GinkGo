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

func TestVoiceStreamModePrecedence(t *testing.T) {
	for _, tc := range []struct {
		name, backend, configuredMode, flagMode, provider, wantMode string
		save, aiEnabled                                             bool
	}{
		{name: "sherpa raw default", save: true, wantMode: "raw"},
		{name: "configured raw", configuredMode: "raw", save: true, aiEnabled: true, wantMode: "raw"},
		{name: "configured ai", configuredMode: "ai", save: true, aiEnabled: true, wantMode: "ai"},
		{name: "disabled ai delegated to daemon fallback", configuredMode: "ai", save: true, wantMode: "ai"},
		{name: "explicit raw overrides ai", configuredMode: "ai", flagMode: "raw", save: true, wantMode: "raw"},
		{name: "explicit ai overrides raw", configuredMode: "raw", flagMode: "ai", save: true, wantMode: "ai"},
		{name: "provider override", flagMode: "ai", provider: "alternate", save: true, wantMode: "ai"},
		{name: "configured ai captions", configuredMode: "ai", aiEnabled: true, wantMode: "raw"},
		{name: "explicit ai captions", flagMode: "ai", aiEnabled: true, wantMode: "raw"},
		{name: "invalid global mode cannot block captions", configuredMode: "invalid", wantMode: "raw"},
		{name: "whisper ignores configured ai", backend: "whisper", configuredMode: "ai", save: true, aiEnabled: true, wantMode: "raw"},
		{name: "whisper explicit raw", backend: "whisper", configuredMode: "ai", flagMode: "raw", save: true, wantMode: "raw"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, messages := startAICommandFake(t, ipc.Response{OK: true, VoiceStatus: &ipc.VoiceStatus{Active: true}})
			cmd := newVoiceCmd()
			cmd.SetContext(ctx)
			backend := tc.backend
			if backend == "" {
				backend = "sherpa"
			}
			cfg := getApp(cmd).Cfg
			cfg.Set("voice.stream.backend", backend)
			cfg.Set("voice.mode", tc.configuredMode)
			cfg.Set("ai.enabled", tc.aiEnabled)
			cfg.Set("ai.provider", "configured-provider")
			args := []string{"stream", "--output", "json", "--trigger", "GINKGO"}
			if tc.save {
				args = append(args, "--save")
			}
			if tc.flagMode != "" {
				args = append(args, "--mode", tc.flagMode)
			}
			if tc.provider != "" {
				args = append(args, "--provider", tc.provider)
			}
			cmd.SetArgs(args)
			require.NoError(t, cmd.Execute())
			m := <-messages
			require.Equal(t, tc.wantMode, m.Mode)
			require.Equal(t, tc.provider, m.Provider, "empty provider is resolved from daemon config")
			require.Equal(t, tc.save, m.Save)
			require.Empty(t, m.Action, "Sherpa new-note policy belongs to the daemon")
			require.Empty(t, m.Model)
			require.Empty(t, m.Language)
			require.Equal(t, tc.configuredMode, cfg.GetString("voice.mode"), "stream must not mutate configuration")
		})
	}
}

func TestVoiceStreamRejectsInvalidProcessingOptions(t *testing.T) {
	for _, tc := range []struct {
		name, backend, configuredMode, wantError string
		args                                     []string
	}{
		{name: "malformed mode", args: []string{"--mode", "automatic"}, wantError: "--mode must be raw or ai"},
		{name: "empty explicit mode", args: []string{"--mode", ""}, wantError: "--mode must be raw or ai"},
		{name: "invalid configured save mode", configuredMode: "invalid", args: []string{"--save"}, wantError: "--mode must be raw or ai"},
		{name: "raw provider", args: []string{"--save", "--mode", "raw", "--provider", "test"}, wantError: "--provider requires --save and AI mode"},
		{name: "caption provider", configuredMode: "ai", args: []string{"--provider", "test"}, wantError: "--provider requires --save and AI mode"},
		{name: "ai caption provider", args: []string{"--mode", "ai", "--provider", "test"}, wantError: "--provider requires --save and AI mode"},
		{name: "whisper ai", backend: "whisper", args: []string{"--save", "--mode", "ai"}, wantError: "Whisper streams support raw notes only"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, messages := startAICommandFake(t, ipc.Response{})
			cmd := newVoiceCmd()
			cmd.SetContext(ctx)
			backend := tc.backend
			if backend == "" {
				backend = "sherpa"
			}
			getApp(cmd).Cfg.Set("voice.stream.backend", backend)
			getApp(cmd).Cfg.Set("voice.mode", tc.configuredMode)
			cmd.SetArgs(append([]string{"stream", "--trigger", "GINKGO"}, tc.args...))
			require.ErrorContains(t, cmd.Execute(), tc.wantError)
			select {
			case <-messages:
				t.Fatal("invalid processing options reached daemon")
			default:
			}
		})
	}
}

func TestVoiceStreamAITextDoesNotPromiseProviderSuccess(t *testing.T) {
	ctx, messages := startAICommandFake(t, ipc.Response{OK: true, VoiceStatus: &ipc.VoiceStatus{Active: true}})
	cmd := newVoiceCmd()
	cmd.SetContext(ctx)
	getApp(cmd).Cfg.Set("voice.stream.backend", "sherpa")
	getApp(cmd).Cfg.Set("voice.mode", "ai")
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"stream", "--save"})
	require.NoError(t, cmd.Execute())
	require.Equal(t, "ai", (<-messages).Mode)
	require.Contains(t, out.String(), "AI note capture requested")
	require.Contains(t, out.String(), "raw fallback if AI is disabled or unavailable")
}
