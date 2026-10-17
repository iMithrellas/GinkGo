package voiceevents

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func watchWire(t *testing.T, payload []byte) ([]Event, error) {
	t.Helper()
	path := testSocket(t)
	listener, err := net.Listen("unix", path)
	require.NoError(t, err)
	defer listener.Close()
	require.NoError(t, os.Chmod(path, 0o600))
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetWriteDeadline(time.Now().Add(time.Second))
		_, _ = conn.Write(payload)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var events []Event
	err = Watch(ctx, path, func(e Event) error { events = append(events, e); return nil })
	<-done
	return events, err
}

func wireJSON(t *testing.T, value any) []byte {
	t.Helper()
	b, err := json.Marshal(value)
	require.NoError(t, err)
	return append(b, '\n')
}

func TestWatchRequiredWireFields(t *testing.T) {
	h := newTestHub(t, Options{})
	snapshot := h.Snapshot()
	for _, field := range []string{
		"type", "schema_version", "epoch_id", "seq", "observed_at",
		"listening", "recording_note", "active_note_session_id", "text", "final",
	} {
		for _, mutation := range []string{"omitted", "null", "wrong_type"} {
			t.Run(field+"/"+mutation, func(t *testing.T) {
				var fields map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(wireJSON(t, snapshot), &fields))
				switch mutation {
				case "omitted":
					delete(fields, field)
				case "null":
					fields[field] = json.RawMessage("null")
				case "wrong_type":
					fields[field] = json.RawMessage("{}")
				}
				events, err := watchWire(t, wireJSON(t, fields))
				require.Error(t, err)
				require.Empty(t, events, "malformed event reached consumer")
			})
		}
	}
	t.Run("reviewer_reproduction", func(t *testing.T) {
		events, err := watchWire(t, []byte("{\"type\":\"state\",\"schema_version\":1,\"epoch_id\":\"e\",\"observed_at\":\"2026-10-17T00:00:00Z\"}\n"))
		require.Error(t, err)
		require.Empty(t, events)
	})
}

func TestWatchTypeSpecificWireFields(t *testing.T) {
	h := newTestHub(t, Options{})
	snapshot := h.Snapshot()
	for _, tc := range []struct {
		typeName string
		fields   map[string]any
		required []string
	}{
		{"transcript", map[string]any{"utterance_id": "u", "revision": 1}, []string{"utterance_id", "revision", "text", "final"}},
		{"note.saved", map[string]any{"note_session_id": "s", "note_id": "n"}, []string{"note_session_id", "note_id"}},
		{"note.canceled", map[string]any{"note_session_id": "s"}, []string{"note_session_id"}},
		{"note.error", map[string]any{"note_session_id": "s", "error": "failure"}, []string{"note_session_id", "error"}},
		{"error", map[string]any{"error": "failure"}, []string{"error"}},
		{"level", map[string]any{"speech": false}, []string{"speech"}},
	} {
		for _, field := range tc.required {
			for _, mutation := range []string{"omitted", "null", "wrong_type"} {
				t.Run(tc.typeName+"/"+field+"/"+mutation, func(t *testing.T) {
					var fields map[string]any
					require.NoError(t, json.Unmarshal(wireJSON(t, snapshot), &fields))
					fields["type"], fields["seq"] = tc.typeName, 1
					for k, v := range tc.fields {
						fields[k] = v
					}
					switch mutation {
					case "omitted":
						delete(fields, field)
					case "null":
						fields[field] = nil
					case "wrong_type":
						fields[field] = map[string]any{}
					}
					payload := append(wireJSON(t, snapshot), wireJSON(t, fields)...)
					events, err := watchWire(t, payload)
					require.Error(t, err)
					require.Len(t, events, 1, "malformed event reached consumer")
					require.Equal(t, "state", events[0].Type)
				})
			}
		}
	}
}

func TestWatchAcceptsExplicitWireZeroValues(t *testing.T) {
	h := newTestHub(t, Options{})
	snapshot := h.Snapshot()
	zero := int64(0)
	level := float64(0)
	speech := false
	for _, input := range []Event{
		{Type: "state"},
		{Type: "transcript", UtteranceID: "u", Revision: 1, Text: "", Final: false, AudioStartMS: &zero, AudioEndMS: &zero},
		{Type: "level", Level: &level},
		{Type: "level", Speech: &speech},
		{Type: "note.saved", NoteSessionID: "s", NoteID: "n"},
		{Type: "note.canceled", NoteSessionID: "s"},
		{Type: "note.error", NoteSessionID: "s", Error: "failure"},
		{Type: "error", Error: "failure"},
	} {
		t.Run(input.Type, func(t *testing.T) {
			input.SchemaVersion, input.EpochID, input.ObservedAt, input.Seq = SchemaVersion, snapshot.EpochID, snapshot.ObservedAt, 1
			events, err := watchWire(t, append(wireJSON(t, snapshot), wireJSON(t, input)...))
			require.ErrorContains(t, err, "disconnected")
			require.Len(t, events, 2)
			require.Zero(t, events[0].Seq)
			require.False(t, events[1].Listening)
			require.False(t, events[1].RecordingNote)
			require.Empty(t, events[1].ActiveNoteSessionID)
			require.Empty(t, events[1].Text)
			require.False(t, events[1].Final)
		})
	}
}

func TestWatchRejectsInvalidTranscriptValues(t *testing.T) {
	h := newTestHub(t, Options{})
	snapshot := h.Snapshot()
	for _, tc := range []struct {
		field string
		value any
	}{
		{"utterance_id", ""}, {"revision", 0}, {"revision", -1}, {"revision", 1.5},
		{"audio_start_ms", nil}, {"audio_end_ms", nil}, {"note_session_id", nil},
	} {
		t.Run(tc.field, func(t *testing.T) {
			var fields map[string]any
			require.NoError(t, json.Unmarshal(wireJSON(t, snapshot), &fields))
			fields["type"], fields["seq"], fields["utterance_id"], fields["revision"] = "transcript", 1, "u", 1
			fields[tc.field] = tc.value
			events, err := watchWire(t, append(wireJSON(t, snapshot), wireJSON(t, fields)...))
			require.Error(t, err)
			require.Len(t, events, 1)
		})
	}
}
