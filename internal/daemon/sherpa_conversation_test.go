package daemon

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mithrel/ginkgo/internal/ai"
	"github.com/mithrel/ginkgo/internal/conversation"
	"github.com/mithrel/ginkgo/internal/ipc"
	"github.com/mithrel/ginkgo/internal/voiceevents"
	"github.com/mithrel/ginkgo/pkg/api"
)

func TestSherpaConversationClarificationDoesNotSave(t *testing.T) {
	v, log, _ := sherpaRuntime(t, "normal")
	app := sherpaAIApp(t, v)
	v.ai = ai.NewService(app.Store.Entries, func(string) (ai.Provider, error) {
		return listenAIProvider(func(context.Context, ai.Request) (ai.Response, error) {
			return ai.Response{Text: `{"action":"clarify","query":"","targets":[],"question":"Which meeting note?"}`}, nil
		}), nil
	}, ai.ServiceOptions{Enabled: true})
	v.conversation = conversation.NewService(app.Store.Entries, v.ai)
	require.True(t, v.stream(context.Background(), ipc.Message{Save: true, Mode: "ai"}).OK)
	log.wait(t, func(e voiceevents.Event) bool { return e.RecordingNote })
	require.True(t, v.stopListener(true).OK)
	response := log.wait(t, func(e voiceevents.Event) bool { return e.Type == "conversation.response" })
	require.Equal(t, "Which meeting note?", response.Text)
	require.Equal(t, "clarify", response.Action)
	require.NotEmpty(t, response.StreamID)
	require.NotEmpty(t, response.CaptureEpochID)
	require.Equal(t, "ai", response.Mode)
	require.Empty(t, response.NoteID)
	entries, _, err := app.Store.Entries.ListEntries(context.Background(), api.ListQuery{})
	require.NoError(t, err)
	require.Empty(t, entries)
}
