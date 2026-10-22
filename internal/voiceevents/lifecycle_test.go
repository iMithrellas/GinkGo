package voiceevents

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSocketLifecycleConversationAndReconnect(t *testing.T) {
	h := newTestHub(t, Options{})
	state := State{CaptureState: "renewing", StreamID: "stream", CaptureEpochID: "first", Namespace: "work", Mode: "ai"}
	require.NoError(t, h.UpdateState(state))
	path, _ := startTestServer(t, h)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	finished := errors.New("done")
	var received []Event
	err := Watch(ctx, path, func(e Event) error {
		received = append(received, e)
		switch len(received) {
		case 1:
			require.Equal(t, state, e.State)
			state.CaptureState, state.CaptureEpochID, state.Listening = "streaming", "second", true
			return h.UpdateState(state)
		case 2:
			return h.Publish(Event{Type: "conversation.response", NoteSessionID: "stream/first/selection", Text: "Which note?", Action: "clarify"})
		case 3:
			return h.Publish(Event{Type: "conversation.response", NoteSessionID: "stream/second/selection", Text: "Found two notes", Action: "search", NoteIDs: []string{"note-2", "note-1"}})
		case 4:
			return h.Publish(Event{Type: "note.saved", NoteSessionID: "stream/second/selection", NoteID: "note-2", Action: "append", Text: "Added to meeting note", Processing: "ai"})
		default:
			return finished
		}
	})
	require.ErrorIs(t, err, finished)
	require.Len(t, received, 5)
	require.Equal(t, "clarify", received[2].Action)
	require.Equal(t, []string{"note-2", "note-1"}, received[3].NoteIDs)
	require.Equal(t, "append", received[4].Action)
	for _, e := range received {
		require.Equal(t, received[0].EpochID, e.EpochID)
		require.Equal(t, "stream", e.StreamID)
	}
	err = Watch(ctx, path, func(e Event) error {
		require.Equal(t, state, e.State)
		require.Equal(t, "state", e.Type)
		require.Empty(t, e.Text)
		require.Empty(t, e.Action)
		require.Empty(t, e.NoteIDs)
		return finished
	})
	require.ErrorIs(t, err, finished)
}

func TestLifecycleAndConversationValidation(t *testing.T) {
	for _, state := range []State{{CaptureState: "unknown"}, {CaptureState: "idle", CaptureError: "error"}, {Mode: "other"}, {StreamID: strings.Repeat("s", 257)}} {
		require.Error(t, validateState(state))
	}
	for _, event := range []Event{
		{Type: "state", Action: "append"},
		{Type: "conversation.response", NoteSessionID: "s", Text: "result", Action: "create"},
		{Type: "note.saved", NoteSessionID: "s", NoteID: "n", Action: "read"},
		{Type: "conversation.response", NoteSessionID: "s", Text: "result", NoteIDs: []string{"same", "same"}},
	} {
		require.Error(t, validateEvent(event))
	}
	base, _ := json.Marshal(Event{Type: "state"})
	for _, field := range []string{"capture_state", "stream_id", "capture_epoch_id", "namespace", "mode", "capture_error", "action", "note_ids"} {
		for _, value := range []string{"null", "42", "false"} {
			_, err := decodeWireEvent([]byte(strings.TrimSuffix(string(base), "}") + `,"` + field + `":` + value + `}`))
			require.Error(t, err, "%s=%s", field, value)
		}
	}
}
