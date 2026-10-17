package voiceworker

import (
	"encoding/json"
	"strings"
	"testing"
)

func testConfig() Config {
	g := Graph{Encoder: "/private/encoder", Decoder: "/private/decoder", Joiner: "/private/joiner", Tokens: "/private/tokens"}
	return Config{Enabled: true, RecorderExecutable: "/private/recorder", Parakeet: g, KWS: g, KeywordsFile: "/private/keywords", SileroModel: "/private/silero", Provider: Auto, AllowCPUFallback: true, RecordingEnabled: true, UpdateMS: 100, WindowMS: 1000, SilenceMS: 500, MaxRecordingMS: 60000, TriggerTimeoutMS: 1000, PreRollMS: 100}
}

func readyEvent() Event {
	return Event{Version: Version, Type: "ready", Epoch: "epoch", Sequence: 1, Provider: &ProviderStatus{Requested: Auto, Effective: Unknown}}
}
func wire(t *testing.T, e Event) []byte {
	t.Helper()
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func decode(t *testing.T, v *Validator, e Event) {
	t.Helper()
	if _, err := v.Decode(wire(t, e)); err != nil {
		t.Fatalf("%s: %v", e.Type, err)
	}
}

func TestValidationFlow(t *testing.T) {
	v := NewValidator("epoch", testConfig())
	decode(t, v, readyEvent())
	m := Metadata{Engine: "sherpa-onnx", Model: "parakeet-tdt-0.6b-v2", Language: "en"}
	decode(t, v, Event{Version: 1, Type: "transcript", Epoch: "epoch", Sequence: 2, Transcript: &Transcript{UtteranceID: "ambient", Revision: 1, Final: true, Text: "caption, not a note", Audio: Span{0, 100}, Metadata: m}})
	decode(t, v, Event{Version: 1, Type: "wake", Epoch: "epoch", Sequence: 3, RecordingNote: true, SessionID: "note-1", UtteranceID: "u1", Audio: &Span{100, 200}})
	decode(t, v, Event{Version: 1, Type: "state", Epoch: "epoch", Sequence: 4, SessionID: "note-1"})
	c := &Candidate{SessionID: "note-1", UtteranceID: "u1", Original: "take a note hello", Body: "hello", Audio: Span{100, 1000}, Control: &Span{100, 300}, ControlText: "take a note", Metadata: m}
	decode(t, v, Event{Version: 1, Type: "note_candidate", Epoch: "epoch", Sequence: 5, Candidate: c})
	decode(t, v, Event{Version: 1, Type: "stopped", Epoch: "epoch", Sequence: 6})
	if _, err := v.Decode(wire(t, Event{Version: 1, Type: "state", Epoch: "epoch", Sequence: 7})); err == nil {
		t.Fatal("event after terminal accepted")
	}
}

func TestStrictEvents(t *testing.T) {
	valid := string(wire(t, readyEvent()))
	cases := map[string]string{
		"missing sequence":  strings.Replace(valid, `"sequence":1,`, "", 1),
		"missing recording": strings.Replace(valid, `"recording_note":false,`, "", 1),
		"null":              strings.Replace(valid, `"recording_note":false`, `"recording_note":null`, 1),
		"wrong bool":        strings.Replace(valid, `"recording_note":false`, `"recording_note":"false"`, 1),
		"duplicate":         strings.Replace(valid, `"version":1`, `"version":1,"version":1`, 1),
		"case alias":        strings.Replace(valid, `"version":1`, `"version":1,"Version":1`, 1),
		"version":           strings.Replace(valid, `"version":1`, `"version":2`, 1),
		"epoch":             strings.Replace(valid, `"epoch":"epoch"`, `"epoch":"other"`, 1),
		"missing status":    strings.Replace(valid, `"fallback_reason":""`, `"other":""`, 1),
		"null status":       strings.Replace(valid, `"effective":"unknown"`, `"effective":null`, 1),
		"unknown payload":   strings.Replace(valid, `"type":"ready"`, `"type":"ready","code":"internal"`, 1),
		"oversized":         strings.Repeat("x", MaxEventBytes+1),
		"trailing":          valid + " {}",
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := NewValidator("epoch", testConfig()).Decode([]byte(input)); err == nil {
				t.Fatal("accepted invalid event")
			}
		})
	}
	if _, err := NewValidator("epoch", testConfig()).Decode([]byte(`{"version":1,"type":"state","epoch":"epoch","sequence":1,"recording_note":false}`)); err == nil {
		t.Fatal("accepted before ready")
	}
}

func TestProviderHonesty(t *testing.T) {
	for _, tc := range []struct {
		requested, effective Provider
		allow                bool
		reason               string
		ok                   bool
	}{
		{Auto, Unknown, false, "", true}, {Auto, CUDA, false, "", true}, {CUDA, CPU, false, "cuda_unavailable", false},
		{Auto, CPU, true, "", false}, {Auto, CPU, true, "cuda_unavailable", true}, {CPU, CUDA, true, "", false}, {CUDA, CUDA, true, "native /secret/model", false},
	} {
		c := testConfig()
		c.Provider = tc.requested
		c.AllowCPUFallback = tc.allow
		v := NewValidator("epoch", c)
		e := readyEvent()
		e.Provider = &ProviderStatus{tc.requested, tc.effective, tc.reason}
		_, err := v.Decode(wire(t, e))
		if (err == nil) != tc.ok {
			t.Errorf("%+v: %v", tc, err)
		}
	}
}

func TestCandidateAuthorizationAndReplay(t *testing.T) {
	m := Metadata{"sherpa-onnx", "parakeet-tdt-0.6b-v2", "en"}
	for _, mutation := range []string{"no wake", "disabled", "changed body", "duplicate", "missing id", "null final"} {
		t.Run(mutation, func(t *testing.T) {
			c := testConfig()
			if mutation == "disabled" {
				c.RecordingEnabled = false
			}
			v := NewValidator("epoch", c)
			decode(t, v, readyEvent())
			seq := uint64(2)
			if mutation != "no wake" {
				_, err := v.Decode(wire(t, Event{Version: 1, Type: "wake", Epoch: "epoch", Sequence: seq, RecordingNote: true, SessionID: "n", UtteranceID: "u", Audio: &Span{0, 10}}))
				if mutation == "disabled" {
					if err == nil {
						t.Fatal("wake accepted")
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				seq++
			}
			candidate := &Candidate{SessionID: "n", UtteranceID: "u", Original: "hello", Body: "hello", Audio: Span{0, 100}, Metadata: m}
			if mutation == "changed body" {
				candidate.Body = "edited"
			}
			if mutation == "missing id" {
				candidate.UtteranceID = ""
			}
			e := Event{Version: 1, Type: "note_candidate", Epoch: "epoch", Sequence: seq, Candidate: candidate}
			if mutation == "null final" {
				b := strings.Replace(string(wire(t, e)), `"body":"hello"`, `"body":null`, 1)
				if _, err := v.Decode([]byte(b)); err == nil {
					t.Fatal("null accepted")
				}
				return
			}
			if mutation == "duplicate" {
				decode(t, v, e)
				e.Sequence++
			}
			if _, err := v.Decode(wire(t, e)); err == nil {
				t.Fatal("unauthorized candidate accepted")
			}
		})
	}
}

func TestConfigAndCommands(t *testing.T) {
	if err := testConfig().Validate(); err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []func(*Config){func(c *Config) { c.RecorderExecutable = "relative" }, func(c *Config) { c.Provider = "gpu" }, func(c *Config) { c.UpdateMS = -1 }, func(c *Config) { c.PreRollMS = 30001 }, func(c *Config) { c.AttemptProvider = CPU }} {
		c := testConfig()
		mutation(&c)
		if c.Validate() == nil {
			t.Fatal("accepted config")
		}
	}
	for _, kind := range []string{"init", "start", "stop"} {
		cmd := Command{Version: 1, Type: kind, Epoch: "epoch"}
		if kind == "init" {
			c := testConfig()
			cmd.Config = &c
		}
		b, _ := json.Marshal(cmd)
		if _, err := DecodeCommand(b); err != nil {
			t.Fatal(err)
		}
	}
}
