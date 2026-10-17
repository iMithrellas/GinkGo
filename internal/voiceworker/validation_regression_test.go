package voiceworker

import (
	"encoding/json"
	"math"
	"testing"
)

func selectedValidator(t *testing.T) *Validator {
	t.Helper()
	v := NewValidator("epoch", testConfig())
	decode(t, v, readyEvent())
	decode(t, v, Event{Version: 1, Type: "wake", Epoch: "epoch", Sequence: 2, RecordingNote: true, SessionID: "selected", UtteranceID: "selected-u", Audio: &Span{1000, 2000}})
	return v
}

func selectedCandidate() Event {
	return Event{Version: 1, Type: "note_candidate", Epoch: "epoch", Sequence: 3, Candidate: &Candidate{SessionID: "selected", UtteranceID: "selected-u", Original: "take a note hello", Body: "take a note hello", Audio: Span{1000, 3000}, Metadata: Metadata{"sherpa-onnx", "parakeet-tdt-0.6b-v2", "en"}}}
}

func TestSelectedCaptureAttribution(t *testing.T) {
	for _, tc := range []struct {
		name  string
		id    string
		audio Span
		ok    bool
	}{
		{"extended capture", "selected-u", Span{1000, 3000}, true},
		{"wake extent only", "selected-u", Span{1000, 2000}, true},
		{"exact maximum", "selected-u", Span{1000, 1000 + 60000*16}, true},
		{"unrelated ID", "unrelated-ambient-u", Span{1000, 3000}, false},
		{"earlier disjoint", "selected-u", Span{0, 100}, false},
		{"later disjoint", "selected-u", Span{3000, 4000}, false},
		{"earlier overlapping start", "selected-u", Span{999, 3000}, false},
		{"later overlapping start", "selected-u", Span{1001, 3000}, false},
		{"shortened wake extent", "selected-u", Span{1000, 1999}, false},
		{"over maximum", "selected-u", Span{1000, 1001 + 60000*16}, false},
		{"huge extent", "selected-u", Span{1000, 1 << 53}, false},
		{"integer boundary", "selected-u", Span{1000, math.MaxInt64}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := selectedValidator(t)
			e := selectedCandidate()
			e.Candidate.UtteranceID = tc.id
			e.Candidate.Audio = tc.audio
			_, err := v.Decode(wire(t, e))
			if (err == nil) != tc.ok {
				t.Fatalf("accepted=%v, want %v: %v", err == nil, tc.ok, err)
			}
		})
	}
}

func TestSelectedCaptureRemainsBoundWhileFinalizing(t *testing.T) {
	v := selectedValidator(t)
	decode(t, v, Event{Version: 1, Type: "state", Epoch: "epoch", Sequence: 3, SessionID: "selected"})
	e := selectedCandidate()
	e.Sequence = 4
	decode(t, v, e)
}

func TestSelectedCaptureControlBounds(t *testing.T) {
	for _, control := range []Span{{0, 1500}, {1000, 3001}, {999, 1001}, {1000, 1000}} {
		v := selectedValidator(t)
		e := selectedCandidate()
		e.Candidate.Control = &control
		e.Candidate.ControlText = "take a note"
		e.Candidate.Body = "hello"
		if _, err := v.Decode(wire(t, e)); err == nil {
			t.Fatalf("accepted control outside selected capture: %+v", control)
		}
	}
	v := selectedValidator(t)
	e := selectedCandidate()
	e.Candidate.Control = &Span{1000, 2000}
	e.Candidate.ControlText = "take a note"
	e.Candidate.Body = "hello"
	decode(t, v, e)
}

func TestWakeExtentAndConfigBounds(t *testing.T) {
	for _, ms := range []int{0, -1, 3600001, int(^uint(0) >> 1)} {
		c := testConfig()
		c.MaxRecordingMS = ms
		v := NewValidator("epoch", c)
		decode(t, v, readyEvent())
		if _, err := v.Decode(wire(t, Event{Version: 1, Type: "wake", Epoch: "epoch", Sequence: 2, RecordingNote: true, SessionID: "s", UtteranceID: "u", Audio: &Span{1000, 2000}})); err == nil {
			t.Fatalf("invalid max duration %d accepted", ms)
		}
	}
	c := testConfig()
	v := NewValidator("epoch", c)
	decode(t, v, readyEvent())
	if _, err := v.Decode(wire(t, Event{Version: 1, Type: "wake", Epoch: "epoch", Sequence: 2, RecordingNote: true, SessionID: "s", UtteranceID: "u", Audio: &Span{0, int64(c.MaxRecordingMS)*16 + 1}})); err == nil {
		t.Fatal("oversized wake accepted")
	}
}

func TestSelectedAndAmbientIDsAreDistinct(t *testing.T) {
	ambient := Event{Version: 1, Type: "transcript", Epoch: "epoch", Sequence: 2, Transcript: &Transcript{UtteranceID: "selected-u", Revision: 1, Final: true, Text: "ambient", Audio: Span{0, 100}, Metadata: Metadata{"sherpa-onnx", "parakeet-tdt-0.6b-v2", "en"}}}
	v := NewValidator("epoch", testConfig())
	decode(t, v, readyEvent())
	decode(t, v, ambient)
	wake := Event{Version: 1, Type: "wake", Epoch: "epoch", Sequence: 3, RecordingNote: true, SessionID: "selected", UtteranceID: "selected-u", Audio: &Span{1000, 2000}}
	if _, err := v.Decode(wire(t, wake)); err == nil {
		t.Fatal("ambient ID reused for selected capture")
	}
	v = selectedValidator(t)
	ambient.Sequence = 3
	ambient.RecordingNote = true
	if _, err := v.Decode(wire(t, ambient)); err == nil {
		t.Fatal("selected ID reused for ambient caption")
	}
	v = selectedValidator(t)
	decode(t, v, selectedCandidate())
	wake.Sequence = 4
	wake.SessionID = "different-session"
	if _, err := v.Decode(wire(t, wake)); err == nil {
		t.Fatal("selected utterance ID reused for later capture")
	}
}

func eventFixture(t *testing.T, kind string) (*Validator, Event) {
	t.Helper()
	e := Event{Version: 1, Type: kind, Epoch: "epoch", Sequence: 2}
	v := NewValidator("epoch", testConfig())
	if kind == "ready" {
		return v, readyEvent()
	}
	decode(t, v, readyEvent())
	switch kind {
	case "state":
	case "level":
		level := 0.0
		e.Level = &level
	case "wake":
		e.RecordingNote = true
		e.SessionID = "selected"
		e.UtteranceID = "selected-u"
		e.Audio = &Span{1000, 2000}
	case "transcript":
		e.Transcript = &Transcript{UtteranceID: "ambient", Revision: 1, Final: false, Text: "", Audio: Span{0, 100}, Metadata: Metadata{"sherpa-onnx", "parakeet-tdt-0.6b-v2", "en"}}
	case "note_candidate":
		return selectedValidator(t), selectedCandidate()
	case "note_canceled":
		return selectedValidator(t), Event{Version: 1, Type: kind, Epoch: "epoch", Sequence: 3, SessionID: "selected", Code: "user"}
	case "error":
		e.Code = "internal"
	case "stopped":
	default:
		t.Fatalf("bad fixture kind %s", kind)
	}
	return v, e
}

func TestForbiddenEventKeyPresence(t *testing.T) {
	allowed := map[string][]string{
		"ready": {"provider"}, "state": {"session_id"}, "level": {"level"}, "wake": {"session_id", "utterance_id", "audio"}, "transcript": {"transcript"}, "note_candidate": {"candidate"}, "note_canceled": {"session_id", "code"}, "error": {"code"}, "stopped": {},
	}
	zeros := map[string]any{"provider": ProviderStatus{}, "session_id": "", "utterance_id": "", "audio": Span{}, "level": 0, "transcript": Transcript{}, "candidate": Candidate{}, "code": ""}
	for kind, keys := range allowed {
		for field, zero := range zeros {
			permitted := false
			for _, key := range keys {
				if key == field {
					permitted = true
				}
			}
			if permitted {
				continue
			}
			encoded, err := json.Marshal(zero)
			if err != nil {
				t.Fatal(err)
			}
			for _, value := range []string{string(encoded), "null"} {
				t.Run(kind+"/"+field+"/"+value, func(t *testing.T) {
					v, e := eventFixture(t, kind)
					var raw map[string]json.RawMessage
					if err := json.Unmarshal(wire(t, e), &raw); err != nil {
						t.Fatal(err)
					}
					raw[field] = json.RawMessage(value)
					data, err := json.Marshal(raw)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := v.Decode(data); err == nil {
						t.Fatal("forbidden field accepted by presence")
					}
				})
			}
		}
	}
}

func TestPermittedZeroValues(t *testing.T) {
	for _, kind := range []string{"ready", "state", "level", "transcript"} {
		t.Run(kind, func(t *testing.T) {
			v, e := eventFixture(t, kind)
			data := wire(t, e)
			if kind == "state" {
				var raw map[string]json.RawMessage
				_ = json.Unmarshal(data, &raw)
				raw["session_id"] = json.RawMessage(`""`)
				data, _ = json.Marshal(raw)
			}
			if _, err := v.Decode(data); err != nil {
				t.Fatal(err)
			}
		})
	}
}
