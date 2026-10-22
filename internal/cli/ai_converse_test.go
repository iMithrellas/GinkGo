package cli

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mithrel/ginkgo/internal/ipc"
)

func TestConversationCommand(t *testing.T) {
	ctx, messages := startAICommandFake(t, ipc.Response{OK: true, Msg: "Which meeting?", Action: "clarify"})
	cmd := newAICmd()
	cmd.SetContext(ctx)
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"converse", "--session", "meeting", "Append to the meeting note"})
	require.NoError(t, cmd.Execute())
	m := <-messages
	require.Equal(t, "ai.converse", m.Name)
	require.Equal(t, "meeting", m.ID)
	require.Equal(t, "work", m.Namespace)
	require.Equal(t, "Append to the meeting note", m.Text)
	require.Equal(t, "Which meeting?\n", output.String())
}
