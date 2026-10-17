// Package voiceworker implements the private, versioned ginkgo-voice transport.
// It does not capture audio, load models, store notes, or publish public events.
package voiceworker

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"unicode/utf8"
)

// Version is the sole supported wire protocol version.
const Version = 1

// MaxEventBytes bounds one newline-delimited JSON message, excluding newline.
const MaxEventBytes = 64 * 1024

// Provider selects inference execution. Auto prefers CUDA, subject to fallback.
type Provider string

const (
	CPU     Provider = "cpu"
	CUDA    Provider = "cuda"
	Auto    Provider = "auto"
	Unknown Provider = "unknown"
)

// Graph names trusted installed files; these paths must never enter diagnostics.
type Graph struct {
	Encoder string `json:"encoder"`
	Decoder string `json:"decoder"`
	Joiner  string `json:"joiner"`
	Tokens  string `json:"tokens"`
}

// Config is sent once in init. All durations are integer milliseconds.
// Audio is always 16000 Hz, mono, signed little-endian 16-bit PCM.
type Config struct {
	Enabled            bool     `json:"enabled"`
	RecorderExecutable string   `json:"recorder_executable"`
	RecorderTarget     string   `json:"recorder_target"`
	Parakeet           Graph    `json:"parakeet"`
	KWS                Graph    `json:"kws"`
	KeywordsFile       string   `json:"keywords_file"`
	SileroModel        string   `json:"silero_model"`
	Provider           Provider `json:"provider"`
	// AttemptProvider is normally empty; CPU is a supervisor-owned pre-capture retry.
	AttemptProvider  Provider `json:"attempt_provider,omitempty"`
	FallbackReason   string   `json:"fallback_reason,omitempty"`
	AllowCPUFallback bool     `json:"allow_cpu_fallback"`
	RecordingEnabled bool     `json:"recording_enabled"`
	SelectedKeyword  string   `json:"selected_keyword,omitempty"`
	UpdateMS         int      `json:"update_ms"`
	WindowMS         int      `json:"window_ms"`
	SilenceMS        int      `json:"silence_ms"`
	MaxRecordingMS   int      `json:"max_recording_ms"`
	TriggerTimeoutMS int      `json:"trigger_timeout_ms"`
	CooldownMS       int      `json:"cooldown_ms"`
	PreRollMS        int      `json:"pre_roll_ms"`
}

// Validate checks configuration without filesystem access or native probing.
func (c Config) Validate() error {
	if !c.Enabled {
		return nil
	}
	for _, p := range []string{c.RecorderExecutable, c.Parakeet.Encoder, c.Parakeet.Decoder, c.Parakeet.Joiner, c.Parakeet.Tokens, c.KWS.Encoder, c.KWS.Decoder, c.KWS.Joiner, c.KWS.Tokens, c.KeywordsFile, c.SileroModel} {
		if !filepath.IsAbs(p) || strings.ContainsRune(p, 0) || len(p) > 4096 {
			return errors.New("voiceworker: invalid installed path")
		}
	}
	if c.Provider != CPU && c.Provider != CUDA && c.Provider != Auto {
		return errors.New("voiceworker: invalid provider")
	}
	if c.AttemptProvider != "" || c.FallbackReason != "" {
		if c.AttemptProvider != CPU || c.Provider == CPU || !c.AllowCPUFallback || c.FallbackReason != "cuda_init_failed" {
			return errors.New("voiceworker: invalid fallback attempt")
		}
	}
	for _, d := range []int{c.UpdateMS, c.WindowMS, c.SilenceMS, c.MaxRecordingMS, c.TriggerTimeoutMS} {
		if d < 1 || d > 3600000 {
			return errors.New("voiceworker: duration outside bounds")
		}
	}
	if c.CooldownMS < 0 || c.CooldownMS > 60000 || c.PreRollMS < 0 || c.PreRollMS > 30000 || c.WindowMS < c.UpdateMS || c.SilenceMS > c.MaxRecordingMS || len(c.SelectedKeyword) > 128 || len(c.RecorderTarget) > 1024 {
		return errors.New("voiceworker: invalid capture settings")
	}
	return nil
}

// ExecutionProvider is the provider for this process's native initialization.
// Provider remains the original requested policy for status reporting.
func (c Config) ExecutionProvider() Provider {
	if c.AttemptProvider != "" {
		return c.AttemptProvider
	}
	return c.Provider
}

// Command is parent-to-child NDJSON. Init carries config; stop carries no config.
// Epoch is a random parent-owned run identifier, not a persistent user identity.
type Command struct {
	Version int     `json:"version"`
	Type    string  `json:"type"`
	Epoch   string  `json:"epoch"`
	Config  *Config `json:"config,omitempty"`
}

// ProviderStatus reports runtime evidence, not GPU placement guarantees.
// FallbackReason is a stable code: empty, cuda_unavailable, or cuda_failed.
type ProviderStatus struct {
	Requested      Provider `json:"requested"`
	Effective      Provider `json:"effective"`
	FallbackReason string   `json:"fallback_reason"`
}

// Span is a half-open interval of 16kHz audio samples since this epoch began.
type Span struct {
	Start int64 `json:"start"`
	End   int64 `json:"end"`
}

// Metadata identifies recognition, never private model filenames.
type Metadata struct {
	Engine   string `json:"engine"`
	Model    string `json:"model"`
	Language string `json:"language"`
}

// Transcript is an ambient caption; even a final is never save authorization.
type Transcript struct {
	UtteranceID string   `json:"utterance_id"`
	Revision    uint64   `json:"revision"`
	Final       bool     `json:"final"`
	Text        string   `json:"text"`
	Audio       Span     `json:"audio"`
	Metadata    Metadata `json:"metadata"`
}

// Candidate is the only potentially persistable payload. Parent must rebind
// trusted metadata and enforce opt-in. Original is the exact selected-audio ASR;
// Body equals Original unless Control safely identifies an excluded prefix.
// SessionID and UtteranceID must match the pending wake. Audio.Start equals
// wake.Audio.Start; Audio.End covers wake.Audio.End. The full selected span,
// including pre-roll and silence, cannot exceed Config.MaxRecordingMS * 16.
type Candidate struct {
	SessionID   string   `json:"session_id"`
	UtteranceID string   `json:"utterance_id"`
	Original    string   `json:"original"`
	Body        string   `json:"body"`
	Audio       Span     `json:"audio"`
	Control     *Span    `json:"control,omitempty"`
	ControlText string   `json:"control_text,omitempty"`
	Metadata    Metadata `json:"metadata"`
}

// Event is child-to-parent NDJSON. Common fields are mandatory and non-null.
// Only the matching payload is permitted. State changes (including acoustic
// wake recording transitions) must not wait for ASR finalization.
type Event struct {
	Version       int             `json:"version"`
	Type          string          `json:"type"`
	Epoch         string          `json:"epoch"`
	Sequence      uint64          `json:"sequence"`
	RecordingNote bool            `json:"recording_note"`
	Provider      *ProviderStatus `json:"provider,omitempty"`
	Level         *float64        `json:"level,omitempty"`
	SessionID     string          `json:"session_id,omitempty"`
	UtteranceID   string          `json:"utterance_id,omitempty"`
	Audio         *Span           `json:"audio,omitempty"`
	Transcript    *Transcript     `json:"transcript,omitempty"`
	Candidate     *Candidate      `json:"candidate,omitempty"`
	Code          string          `json:"code,omitempty"`
}

// DecodeCommand decodes a bounded strict command for helper implementations.
func DecodeCommand(data []byte) (Command, error) {
	var c Command
	if err := strict(data, &c); err != nil {
		return c, err
	}
	if c.Version != Version || !identifier(c.Epoch) {
		return c, errors.New("voiceworker: invalid command envelope")
	}
	switch c.Type {
	case "init":
		if c.Config == nil || !c.Config.Enabled {
			return c, errors.New("voiceworker: missing enabled init config")
		}
		return c, c.Config.Validate()
	case "start", "stop":
		if c.Config != nil {
			return c, errors.New("voiceworker: unexpected command config")
		}
	default:
		return c, errors.New("voiceworker: invalid command type")
	}
	return c, nil
}

func strict(data []byte, v any) error {
	if len(data) > MaxEventBytes || !utf8.Valid(data) {
		return errors.New("voiceworker: oversized message")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return errors.New("voiceworker: malformed message")
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("voiceworker: trailing message data")
	}
	// Reject duplicate keys and nulls everywhere, not just the envelope.
	seen := []map[string]bool{}
	t := json.NewDecoder(bytes.NewReader(data))
	var walk func() error
	walk = func() error {
		x, err := t.Token()
		if err != nil || x == nil {
			return errors.New("voiceworker: null or invalid field")
		}
		if delim, ok := x.(json.Delim); ok {
			if delim != '{' {
				return errors.New("voiceworker: unexpected array")
			}
			seen = append(seen, map[string]bool{})
			m := seen[len(seen)-1]
			for t.More() {
				k, e := t.Token()
				if e != nil {
					return e
				}
				key := k.(string)
				if m[key] {
					return errors.New("voiceworker: duplicate field")
				}
				m[key] = true
				if e = walk(); e != nil {
					return e
				}
			}
			_, err = t.Token()
			seen = seen[:len(seen)-1]
			return err
		}
		return nil
	}
	if err := walk(); err != nil {
		return err
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal(data, &raw) != nil {
		return errors.New("voiceworker: expected object")
	}
	return required(raw, reflect.TypeOf(v).Elem())
}

func required(raw map[string]json.RawMessage, typ reflect.Type) error {
	known := map[string]bool{}
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		tag := strings.Split(f.Tag.Get("json"), ",")
		b, ok := raw[tag[0]]
		known[tag[0]] = true
		if !ok {
			if len(tag) == 1 {
				return errors.New("voiceworker: missing field")
			}
			continue
		}
		t := f.Type
		if t.Kind() == reflect.Pointer {
			t = t.Elem()
		}
		if t.Kind() == reflect.Struct {
			var nested map[string]json.RawMessage
			if json.Unmarshal(b, &nested) != nil {
				return errors.New("voiceworker: invalid object")
			}
			if err := required(nested, t); err != nil {
				return err
			}
		}
	}
	for k := range raw {
		if !known[k] {
			return errors.New("voiceworker: unknown field")
		}
	}
	return nil
}

func identifier(s string) bool {
	if len(s) < 1 || len(s) > 128 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
			return false
		}
	}
	return true
}
