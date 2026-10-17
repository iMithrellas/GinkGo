package voiceworker

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Validator tracks ordering for one epoch. It is not safe for concurrent use.
type Validator struct {
	epoch              string
	config             Config
	sequence           uint64
	ready, terminal    bool
	recording          bool
	active             string
	activeUtterance    string
	activeAudio        Span
	sessions           map[string]bool
	selectedUtterances map[string]bool
	transcripts        map[string]revisionState
}

type revisionState struct {
	revision uint64
	final    bool
}

// NewValidator creates a pure-Go protocol validator without any I/O.
func NewValidator(epoch string, config Config) *Validator {
	return &Validator{epoch: epoch, config: config, sessions: map[string]bool{}, selectedUtterances: map[string]bool{}, transcripts: map[string]revisionState{}}
}

// Decode validates an event before returning it to the caller. Any error aborts
// the stream; a validator must not be reused after an error.
func (v *Validator) Decode(data []byte) (Event, error) {
	var e Event
	bad := func() (Event, error) { return Event{}, errors.New("voiceworker: invalid event") }
	if err := strict(data, &e); err != nil {
		return Event{}, err
	}
	if e.Version != Version || e.Epoch != v.epoch || e.Sequence == 0 || e.Sequence != v.sequence+1 || v.terminal {
		return bad()
	}
	if !v.ready && e.Type != "ready" {
		return bad()
	}
	if !eventFields(data, e.Type) {
		return bad()
	}
	switch e.Type {
	case "ready":
		if v.ready || e.RecordingNote || e.Provider == nil || !v.provider(*e.Provider) {
			return bad()
		}
		v.ready = true
	case "state":
		if e.RecordingNote {
			if !v.recording || e.SessionID == "" || e.SessionID != v.active {
				return bad()
			}
		} else if e.SessionID != "" && e.SessionID != v.active {
			return bad()
		}
		v.recording = e.RecordingNote
	case "level":
		if e.Level == nil || math.IsNaN(*e.Level) || *e.Level < 0 || *e.Level > 1 {
			return bad()
		}
	case "wake":
		_, ambientID := v.transcripts[e.UtteranceID]
		if !v.config.RecordingEnabled || !e.RecordingNote || v.active != "" || !identifier(e.SessionID) || !identifier(e.UtteranceID) || e.Audio == nil || !v.selectedSpan(*e.Audio) || v.sessions[e.SessionID] || v.selectedUtterances[e.UtteranceID] || ambientID || len(v.sessions) >= 4096 {
			return bad()
		}
		v.active = e.SessionID
		v.activeUtterance = e.UtteranceID
		v.activeAudio = *e.Audio
		v.recording = true
		v.sessions[e.SessionID] = true
		v.selectedUtterances[e.UtteranceID] = true
	case "transcript":
		t := e.Transcript
		if t == nil || !identifier(t.UtteranceID) || v.selectedUtterances[t.UtteranceID] || t.Revision == 0 || !text(t.Text) || !span(&t.Audio) || !metadata(t.Metadata) {
			return bad()
		}
		prev, exists := v.transcripts[t.UtteranceID]
		if exists && (prev.final || t.Revision <= prev.revision) || !exists && len(v.transcripts) >= 4096 {
			return bad()
		}
		v.transcripts[t.UtteranceID] = revisionState{t.Revision, t.Final}
	case "note_candidate":
		c := e.Candidate
		if e.RecordingNote || c == nil || v.active == "" || c.SessionID != v.active || c.UtteranceID != v.activeUtterance || c.Audio.Start != v.activeAudio.Start || c.Audio.End < v.activeAudio.End || !text(c.Original) || !text(c.Body) || strings.TrimSpace(c.Body) == "" || !v.selectedSpan(c.Audio) || !metadata(c.Metadata) {
			return bad()
		}
		if c.Control == nil {
			if c.ControlText != "" || c.Body != c.Original {
				return bad()
			}
		} else {
			if !span(c.Control) || c.Control.Start != c.Audio.Start || c.Control.End > c.Audio.End || c.ControlText == "" || !strings.HasPrefix(c.Original, c.ControlText) || c.Body != strings.TrimSpace(strings.TrimPrefix(c.Original, c.ControlText)) {
				return bad()
			}
			last, _ := utf8.DecodeLastRuneInString(c.ControlText)
			next, _ := utf8.DecodeRuneInString(strings.TrimPrefix(c.Original, c.ControlText))
			word := func(r rune) bool { return unicode.IsLetter(r) || unicode.IsNumber(r) }
			if word(last) && word(next) {
				return bad()
			}
		}
		v.active = ""
		v.activeUtterance = ""
		v.activeAudio = Span{}
		v.recording = false
	case "note_canceled":
		if e.RecordingNote || v.active == "" || e.SessionID != v.active || !cancelCode(e.Code) {
			return bad()
		}
		v.active = ""
		v.activeUtterance = ""
		v.activeAudio = Span{}
		v.recording = false
	case "error":
		if !errorCode(e.Code) {
			return bad()
		}
		v.terminal = true
	case "stopped":
		if e.RecordingNote || v.active != "" {
			return bad()
		}
		v.terminal = true
	default:
		return bad()
	}
	if e.Type != "wake" && e.Type != "note_candidate" && e.Type != "note_canceled" && e.Type != "state" && e.RecordingNote != v.recording {
		return bad()
	}
	v.sequence = e.Sequence
	return e, nil
}

// eventFields checks original key presence: omitempty must not erase a forbidden
// payload key just because its value was empty, false, or zero on the wire.
func eventFields(data []byte, kind string) bool {
	allowed := map[string]bool{"version": true, "type": true, "epoch": true, "sequence": true, "recording_note": true}
	var payload []string
	switch kind {
	case "ready":
		payload = []string{"provider"}
	case "state":
		payload = []string{"session_id"}
	case "level":
		payload = []string{"level"}
	case "wake":
		payload = []string{"session_id", "utterance_id", "audio"}
	case "transcript":
		payload = []string{"transcript"}
	case "note_candidate":
		payload = []string{"candidate"}
	case "note_canceled":
		payload = []string{"session_id", "code"}
	case "error":
		payload = []string{"code"}
	case "stopped":
	default:
		return false
	}
	for _, key := range payload {
		allowed[key] = true
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal(data, &raw) != nil {
		return false
	}
	for key := range raw {
		if !allowed[key] {
			return false
		}
	}
	return true
}

func (v *Validator) selectedSpan(s Span) bool {
	// Validate the millisecond bound before multiplication, even when callers
	// construct a standalone Validator without first calling Config.Validate.
	ms := v.config.MaxRecordingMS
	return span(&s) && ms >= 1 && ms <= 3600000 && s.End-s.Start <= int64(ms)*16
}

func (v *Validator) provider(p ProviderStatus) bool {
	if v.config.AttemptProvider == CPU {
		return p.Requested == v.config.Provider && p.Effective == CPU && p.FallbackReason == "cuda_init_failed"
	}
	if p.Requested != v.config.Provider || p.Effective != CPU && p.Effective != CUDA && p.Effective != Unknown {
		return false
	}
	if p.FallbackReason != "" && p.FallbackReason != "cuda_unavailable" && p.FallbackReason != "cuda_failed" {
		return false
	}
	if p.Requested == CPU {
		return (p.Effective == CPU || p.Effective == Unknown) && p.FallbackReason == ""
	}
	if p.Effective == CPU {
		return v.config.AllowCPUFallback && p.FallbackReason != ""
	}
	return p.FallbackReason == ""
}

func span(s *Span) bool { return s != nil && s.Start >= 0 && s.End > s.Start && s.End <= 1<<53 }
func text(s string) bool {
	return len(s) <= 16384 && utf8.ValidString(s) && !strings.ContainsRune(s, 0)
}
func metadata(m Metadata) bool {
	return m.Engine == "sherpa-onnx" && m.Model == "parakeet-tdt-0.6b-v2" && m.Language == "en"
}
func cancelCode(s string) bool {
	return s == "timeout" || s == "empty" || s == "control_only" || s == "user" || s == "shutdown"
}
func errorCode(s string) bool {
	switch s {
	case "capture_failed", "model_failed", "inference_failed", "provider_unavailable", "internal":
		return true
	}
	return false
}
