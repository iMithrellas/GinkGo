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
