package voiceevents

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSavedProcessingMetadata(t *testing.T) {
	for _, processing := range []string{"", "ai", "raw_fallback"} {
		event := Event{Type: "note.saved", NoteSessionID: "session", NoteID: "note", Processing: processing}
		if processing == "raw_fallback" {
			event.FallbackReason = "AI is disabled"
		}
		require.NoError(t, validateEvent(event))
		data, err := json.Marshal(event)
		require.NoError(t, err)
		decoded, err := decodeWireEvent(data)
		require.NoError(t, err)
		require.Equal(t, event, decoded)
		if processing == "" {
			require.NotContains(t, string(data), "processing")
			require.NotContains(t, string(data), "fallback_reason")
		}
	}
	for _, event := range []Event{
		{Type: "state", Processing: "ai"},
		{Type: "note.saved", NoteSessionID: "s", NoteID: "n", Processing: "raw"},
		{Type: "note.saved", NoteSessionID: "s", NoteID: "n", Processing: "raw_fallback"},
		{Type: "note.saved", NoteSessionID: "s", NoteID: "n", FallbackReason: "reason"},
		{Type: "note.saved", NoteSessionID: "s", NoteID: "n", Processing: "ai", FallbackReason: "reason"},
		{Type: "note.saved", NoteSessionID: "s", NoteID: "n", Processing: "raw_fallback", FallbackReason: strings.Repeat("x", 1025)},
	} {
		require.Error(t, validateEvent(event))
	}
	base, err := json.Marshal(Event{Type: "note.saved", NoteSessionID: "s", NoteID: "n"})
	require.NoError(t, err)
	for _, field := range []string{"processing", "fallback_reason"} {
		for _, value := range []string{"null", "7", "false"} {
			data := strings.TrimSuffix(string(base), "}") + `,"` + field + `":` + value + `}`
			_, err := decodeWireEvent([]byte(data))
			require.Error(t, err)
		}
	}
}
