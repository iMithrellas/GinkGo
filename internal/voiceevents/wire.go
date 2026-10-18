package voiceevents

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// decodeWireEvent checks presence before decoding into value fields: JSON null
// and absent fields must not silently become valid false/empty/zero values.
func decodeWireEvent(data []byte) (Event, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return Event{}, err
	}
	for _, name := range []string{
		"type", "schema_version", "epoch_id", "seq", "observed_at",
		"listening", "recording_note", "active_note_session_id", "text", "final",
	} {
		if _, ok := fields[name]; !ok {
			return Event{}, fmt.Errorf("missing required voice event field %q", name)
		}
	}
	// Optional fields may be omitted, but a supplied value must have its wire
	// type, not null. Ignore unknown fields for forward-compatible additions.
	known := make(map[string]json.RawMessage)
	for _, name := range []string{
		"type", "schema_version", "epoch_id", "seq", "observed_at",
		"listening", "recording_note", "active_note_session_id", "text", "final",
		"note_session_id", "utterance_id", "revision", "audio_start_ms", "audio_end_ms",
		"level", "speech", "note_id", "error", "processing", "fallback_reason", "image_status",
	} {
		if raw, ok := fields[name]; ok {
			if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
				return Event{}, fmt.Errorf("null voice event field %q", name)
			}
			known[name] = raw
		}
	}
	var event Event
	// Decode the exact keys checked above, rather than allowing encoding/json's
	// case-insensitive struct matching to override them via differently cased keys.
	canonical, err := json.Marshal(known)
	if err != nil {
		return Event{}, err
	}
	if err := json.Unmarshal(canonical, &event); err != nil {
		return Event{}, err
	}
	return event, nil
}
