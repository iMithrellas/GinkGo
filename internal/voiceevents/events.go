// Package voiceevents provides bounded, read-only live voice subscriptions.
package voiceevents

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"sync"
	"time"
	"unicode/utf8"
)

// SchemaVersion identifies the NDJSON wire contract.
const SchemaVersion = 1

// MaxMessageBytes bounds a complete JSON message, including its newline.
const MaxMessageBytes = 64 * 1024

// State is current global capture state, not attribution of delayed results.
type State struct {
	Listening           bool   `json:"listening"`
	RecordingNote       bool   `json:"recording_note"`
	ActiveNoteSessionID string `json:"active_note_session_id"`
}

// Event is a v1 observation. Envelope and State fields are assigned by Hub.
// NoteSessionID attributes this result independently of current State.
type Event struct {
	Type          string    `json:"type"`
	SchemaVersion int       `json:"schema_version"`
	EpochID       string    `json:"epoch_id"`
	Seq           uint64    `json:"seq"`
	ObservedAt    time.Time `json:"observed_at"`
	State
	NoteSessionID string   `json:"note_session_id,omitempty"`
	UtteranceID   string   `json:"utterance_id,omitempty"`
	Revision      uint64   `json:"revision,omitempty"`
	Text          string   `json:"text"`
	Final         bool     `json:"final"`
	AudioStartMS  *int64   `json:"audio_start_ms,omitempty"`
	AudioEndMS    *int64   `json:"audio_end_ms,omitempty"`
	Level         *float64 `json:"level,omitempty"`
	Speech        *bool    `json:"speech,omitempty"`
	NoteID        string   `json:"note_id,omitempty"`
	Error         string   `json:"error,omitempty"`
}

// Options sets fixed bounds. Zero values select defaults.
type Options struct{ QueueSize, MaxClients int }

type subscription struct {
	queue chan []byte
	done  chan struct{}
}

// Hub serializes state, snapshots and publication without socket IO.
type Hub struct {
	mu      sync.Mutex
	state   State
	epoch   string
	seq     uint64
	options Options
	clients map[*subscription]struct{}
	closed  bool
}

// NewHub creates a fresh stream epoch. Bounds cannot change while running.
func NewHub(options Options) (*Hub, error) {
	if options.QueueSize == 0 {
		options.QueueSize = 32
	}
	if options.MaxClients == 0 {
		options.MaxClients = 16
	}
	if options.QueueSize < 1 || options.QueueSize > 1024 || options.MaxClients < 1 || options.MaxClients > 256 {
		return nil, errors.New("invalid voice event bounds")
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return nil, err
	}
	return &Hub{epoch: hex.EncodeToString(id[:]), options: options, clients: make(map[*subscription]struct{})}, nil
}

func validateState(s State) error {
	if len(s.ActiveNoteSessionID) > 256 || !utf8.ValidString(s.ActiveNoteSessionID) || s.RecordingNote != (s.ActiveNoteSessionID != "") {
		return errors.New("invalid active note session state")
	}
	return nil
}

func validateEvent(e Event) error {
	for _, s := range []string{e.NoteSessionID, e.UtteranceID, e.NoteID} {
		if len(s) > 256 || !utf8.ValidString(s) {
			return errors.New("invalid event identifier")
		}
	}
	if len(e.Text) > 48*1024 || len(e.Error) > 1024 || !utf8.ValidString(e.Text) || !utf8.ValidString(e.Error) {
		return errors.New("invalid event text size or encoding")
	}
	if e.AudioStartMS != nil && *e.AudioStartMS < 0 || e.AudioEndMS != nil && *e.AudioEndMS < 0 || e.AudioStartMS != nil && e.AudioEndMS != nil && *e.AudioEndMS < *e.AudioStartMS {
		return errors.New("invalid audio window")
	}
	if e.Level != nil && (math.IsNaN(*e.Level) || math.IsInf(*e.Level, 0) || *e.Level < 0 || *e.Level > 1) {
		return errors.New("invalid level")
	}
	switch e.Type {
	case "state":
	case "level":
		if e.Level == nil && e.Speech == nil {
			return errors.New("level event requires level or speech")
		}
	case "transcript":
		if e.UtteranceID == "" || e.Revision == 0 {
			return errors.New("transcript requires utterance and positive revision")
		}
	case "note.saved":
		if e.NoteSessionID == "" || e.NoteID == "" {
			return errors.New("saved event requires session and note")
		}
	case "note.canceled":
		if e.NoteSessionID == "" {
			return errors.New("canceled event requires session")
		}
	case "note.error":
		if e.NoteSessionID == "" || e.Error == "" {
			return errors.New("note error requires session and error")
		}
	case "error":
		if e.Error == "" {
			return errors.New("error event requires error")
		}
	default:
		return errors.New("unknown voice event type")
	}
	return nil
}

func (h *Hub) encodeLocked(e Event) ([]byte, error) {
	e.SchemaVersion, e.EpochID, e.Seq, e.State = SchemaVersion, h.epoch, h.seq, h.state
	e.ObservedAt = time.Now().UTC()
	b, err := json.Marshal(e)
	if err != nil {
		return nil, err
	}
	if len(b)+1 > MaxMessageBytes {
		return nil, errors.New("voice event exceeds message bound")
	}
	return append(b, '\n'), nil
}

// Publish stamps an observation with current global state. It never waits for a
// subscriber; any full queue is disconnected, including for partial events.
func (h *Hub) Publish(e Event) error {
	if err := validateEvent(e); err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.publishLocked(e)
}

func (h *Hub) publishLocked(e Event) error {
	if h.closed {
		return errors.New("voice event hub closed")
	}
	h.seq++
	b, err := h.encodeLocked(e)
	if err != nil {
		return err
	}
	for s := range h.clients {
		select {
		case s.queue <- b:
		default:
			h.removeLocked(s)
		}
	}
	return nil
}

// UpdateState atomically replaces global state and publishes a state event.
func (h *Hub) UpdateState(state State) error {
	if err := validateState(state); err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return errors.New("voice event hub closed")
	}
	h.state = state
	return h.publishLocked(Event{Type: "state"})
}

// Snapshot returns a bounded current-state observation without publishing it.
func (h *Hub) Snapshot() Event {
	h.mu.Lock()
	defer h.mu.Unlock()
	return Event{Type: "state", SchemaVersion: SchemaVersion, EpochID: h.epoch, Seq: h.seq, ObservedAt: time.Now().UTC(), State: h.state}
}

func (h *Hub) subscribe() (*subscription, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed || len(h.clients) >= h.options.MaxClients {
		return nil, errors.New("voice subscription unavailable")
	}
	s := &subscription{queue: make(chan []byte, h.options.QueueSize), done: make(chan struct{})}
	b, err := h.encodeLocked(Event{Type: "state"})
	if err != nil {
		return nil, err
	}
	s.queue <- b
	h.clients[s] = struct{}{}
	return s, nil
}

func (h *Hub) removeLocked(s *subscription) {
	if _, ok := h.clients[s]; ok {
		delete(h.clients, s)
		close(s.done)
	}
}

func (h *Hub) unsubscribe(s *subscription) { h.mu.Lock(); defer h.mu.Unlock(); h.removeLocked(s) }

// Close disconnects all subscribers and rejects subsequent publications.
func (h *Hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	for s := range h.clients {
		h.removeLocked(s)
	}
}
