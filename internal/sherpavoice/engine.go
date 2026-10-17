package sherpavoice

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Event is a private helper-domain event, adapted to voiceworker at the process
// boundary. Only note_candidate authorizes selected-note persistence.
type Event struct {
	Type, ID, SessionID            string
	Revision                       uint64
	Final, RecordingNote, Speaking bool
	Span
	Original, Body, Phrase string
	Level                  float64
}

// EngineConfig bounds every audio and inference queue independently.
type EngineConfig struct {
	RingSamples         int
	PreviewSamples      int
	PreviewEvery        int
	MinSpeech           int
	WakeTimeout         int
	Cooldown            int
	FinalQueue          int
	PreRollSamples      int
	RecordingEnabled    bool
	SelectedKeyword     string
	MaxRecordingSamples int
}

// DefaultEngineConfig permits 30-second finals and 5-second preview tails at
// 500 ms cadence. Preview revisions are simulated streaming offline snapshots.
func DefaultEngineConfig() EngineConfig {
	return EngineConfig{RingSamples: 35 * SampleRate, PreviewSamples: 5 * SampleRate, PreviewEvery: SampleRate / 2, MinSpeech: SampleRate / 4, WakeTimeout: 5 * SampleRate, Cooldown: 2 * SampleRate, FinalQueue: 8, PreRollSamples: SampleRate / 2, RecordingEnabled: true, MaxRecordingSamples: 30 * SampleRate}
}

type selection struct {
	id, phrase       string
	utterance        string
	start, deadline  int64
	wakeEnd, lastEnd int64
	hasSpeech        bool
	decided          bool
	recording        bool
	pending          int
}

// Engine processes one capture clock. Process, Poll, Stop and Cancel must be
// called by one owner. The publisher MUST be nonblocking; return a bounded
// output-overflow error rather than waiting for stdout.
type Engine struct {
	config                                 EngineConfig
	front                                  Frontend
	ring                                   *sampleRing
	queue                                  *decodeQueue
	results                                chan decodeResult
	abort                                  chan struct{}
	publish                                func(Event) error
	closed                                 bool
	sequence                               uint64
	captionID                              string
	captionStart, previewAt, cooldownUntil int64
	revisions                              map[string]uint64
	finals                                 map[string]bool
	gate                                   *selection
	authority                              context.Context
	stopCommit                             <-chan struct{}
	stopping                               <-chan struct{}
}

// NewEngine transfers ownership of both backends on success. Model
// initialization must finish before any recorder is started.
func NewEngine(c EngineConfig, f Frontend, r Recognizer, publish func(Event) error) (*Engine, error) {
	if f == nil || r == nil || publish == nil || c.RingSamples < SampleRate || c.RingSamples > 120*SampleRate || c.PreviewSamples <= 0 || c.PreviewSamples > c.RingSamples || c.PreviewEvery <= 0 || c.MinSpeech <= 0 || c.WakeTimeout <= 0 || c.Cooldown < 0 || c.FinalQueue < 1 || c.FinalQueue > 64 {
		return nil, errors.New("invalid engine configuration")
	}
	if c.PreRollSamples < 0 || c.PreRollSamples > c.RingSamples || c.MaxRecordingSamples < c.MinSpeech || c.MaxRecordingSamples > c.RingSamples {
		return nil, errors.New("invalid selected audio bounds")
	}
	e := &Engine{config: c, front: f, ring: newRing(c.RingSamples), queue: newQueue(c.FinalQueue), results: make(chan decodeResult, c.FinalQueue+2), abort: make(chan struct{}), publish: publish, revisions: make(map[string]uint64), finals: make(map[string]bool)}
	e.authority = context.Background()
	go func() {
		defer close(e.results)
		defer r.Close()
		for {
			j, ok := e.queue.get()
			if !ok {
				return
			}
			text, err := r.Decode(j.samples)
			j.samples = nil
			select {
			case e.results <- decodeResult{j, text, err}:
			case <-e.abort:
				return
			}
		}
	}()
	return e, nil
}
func (e *Engine) id(prefix string) string {
	e.sequence++
	return fmt.Sprintf("%s-%d", prefix, e.sequence)
}
func (e *Engine) emit(event Event) error {
	if err := e.authority.Err(); err != nil {
		return err
	}
	if event.Type == "note_candidate" {
		select {
		case <-e.stopping:
			if err := e.commitAuthority(); err != nil {
				return err
			}
		default:
		}
	}
	return e.publish(event)
}

// Process accepts bounded contiguous normalized mono PCM without waiting for
// ASR or stdout. Wake and VAD decisions are independent of ASR transcripts.
func (e *Engine) Process(p []float32) error {
	if e.closed {
		return errors.New("engine closed")
	}
	if len(p) == 0 || len(p) > SampleRate {
		return errors.New("invalid capture frame")
	}
	for _, v := range p {
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) || v < -1 || v > 1 {
			return errors.New("invalid pcm")
		}
	}
	if err := e.Poll(); err != nil {
		return err
	}
	e.ring.append(p)
	wakes, a, err := e.front.Accept(p)
	if err != nil {
		return errors.New("frontend failed")
	}
	for _, w := range wakes {
		if w.Phrase == "" || strings.TrimSpace(w.SpokenForm) == "" || w.Start < e.ring.start() || w.Start < 0 || w.End > e.ring.end || w.Start >= w.End || w.End-w.Start > int64(e.config.MaxRecordingSamples) {
			continue
		}
		if !e.config.RecordingEnabled || e.config.SelectedKeyword != "" && !strings.EqualFold(w.Phrase, e.config.SelectedKeyword) || e.gate != nil || e.ring.end < e.cooldownUntil {
			continue
		}
		noteID := e.id("note")
		e.gate = &selection{id: noteID, utterance: noteID + "-audio", phrase: w.SpokenForm, start: w.Start, wakeEnd: w.End, deadline: e.ring.end + int64(e.config.WakeTimeout), recording: true}
		e.cooldownUntil = e.ring.end + int64(e.config.Cooldown)
		if err := e.emit(Event{Type: "wake", ID: e.gate.utterance, SessionID: e.gate.id, Phrase: w.Phrase, Span: w.Span, RecordingNote: true}); err != nil {
			return err
		}
		if err := e.recording(true, e.gate.id); err != nil {
			return err
		}
	}
	if err := e.activity(a); err != nil {
		return err
	}
	if e.gate != nil && e.gate.recording && a.Speaking {
		e.gate.hasSpeech = true
		e.gate.deadline = e.ring.end + int64(e.config.WakeTimeout)
	}
	if e.gate != nil && e.gate.recording && e.ring.end-e.gate.start >= int64(e.config.MaxRecordingSamples) {
		if e.gate.hasSpeech {
			if err := e.selectFinal(e.gate.start + int64(e.config.MaxRecordingSamples)); err != nil {
				return err
			}
		}
		e.gate.recording = false
		if err := e.recording(false, e.gate.id); err != nil {
			return err
		}
		if e.gate.pending == 0 {
			if err := e.cancelSelection("empty"); err != nil {
				return err
			}
		}
	}
	if e.gate != nil && e.gate.recording && e.ring.end >= e.gate.deadline {
		e.gate.recording = false
		if err := e.recording(false, e.gate.id); err != nil {
			return err
		}
		if e.gate.pending == 0 {
			if err := e.cancelSelection("timeout"); err != nil {
				return err
			}
		}
	}
	if a.Speaking && e.captionID != "" && e.ring.end-e.previewAt >= int64(e.config.PreviewEvery) {
		e.previewAt = e.ring.end
		span := Span{max(e.captionStart, e.ring.end-int64(e.config.PreviewSamples)), e.ring.end}
		if err := e.submit(e.captionID, span, false, nil); err != nil {
			return err
		}
	}
	// Throttled level/speech metadata; never used as the endpoint detector.
	if e.ring.end/int64(e.config.PreviewEvery) != (e.ring.end-int64(len(p)))/int64(e.config.PreviewEvery) {
		var energy float64
		for _, v := range p {
			energy += float64(v) * float64(v)
		}
		if err := e.emit(Event{Type: "activity", Span: Span{e.ring.end - int64(len(p)), e.ring.end}, Speaking: a.Speaking, RecordingNote: e.gate != nil && e.gate.recording, Level: math.Sqrt(energy / float64(len(p)))}); err != nil {
			return err
		}
	}
	return e.Poll()
}
func (e *Engine) recording(on bool, id string) error {
	return e.emit(Event{Type: "recording", SessionID: id, RecordingNote: on, Span: Span{e.ring.end, e.ring.end}})
}
func (e *Engine) activity(a Activity) error {
	for _, span := range a.Segments {
		if span.Start < 0 || span.Start >= span.End || span.End > e.ring.end {
			return errors.New("invalid endpoint")
		}
		// A discarded short pulse must not strand either caption or note gate.
		if span.End-span.Start < int64(e.config.MinSpeech) {
			continue
		}
		if e.captionID == "" {
			e.captionID = e.id("caption")
		}
		if err := e.submit(e.captionID, span, true, nil); err != nil {
			return err
		}
		e.captionID = ""
		if e.gate != nil && e.gate.recording && !e.gate.decided && span.End >= e.gate.wakeEnd {
			e.gate.hasSpeech = true
			if err := e.selectFinal(min(span.End, e.gate.start+int64(e.config.MaxRecordingSamples))); err != nil {
				return err
			}
		}
	}
	if a.Speaking && e.captionID == "" {
		e.captionID = e.id("caption")
		e.captionStart = max(e.ring.start(), e.ring.end-max(int64(e.config.PreRollSamples), 1))
		e.previewAt = e.ring.end
	}
	return nil
}
func (e *Engine) selectFinal(end int64) error {
	s := e.gate
	if end < s.wakeEnd || end <= s.lastEnd || end-s.start < int64(e.config.MinSpeech) {
		return nil
	}
	// The selected clock always begins at the original acoustic wake, including
	// a phrase-alone pause. It never switches to a future ambient utterance.
	if err := e.submit(e.id("selected"), Span{s.start, end}, true, s); err != nil {
		return err
	}
	s.pending++
	s.lastEnd = end
	return nil
}
func (e *Engine) submit(id string, span Span, final bool, sel *selection) error {
	p, err := e.ring.snapshot(span)
	if err != nil {
		return err
	}
	e.revisions[id]++
	j := decodeJob{id: id, span: span, samples: p, final: final, revision: e.revisions[id], selection: sel}
	if err := e.queue.put(j); err != nil {
		return err
	}
	if final {
		e.finals[id] = true
	}
	return nil
}

// Poll publishes ready results without waiting. Revisions superseded by a
// submitted final cannot appear later as stale partials.
func (e *Engine) Poll() error {
	for {
		select {
		case r, ok := <-e.results:
			if !ok {
				return nil
			}
			if err := e.result(r); err != nil {
				return err
			}
		default:
			return nil
		}
	}
}
func (e *Engine) result(r decodeResult) error {
	j := r.job
	if r.err != nil {
		return errors.New("recognition failed")
	}
	if len(r.text) > 16384 || !utf8.ValidString(r.text) || strings.ContainsRune(r.text, 0) {
		return errors.New("invalid recognition text")
	}
	if !j.final && e.finals[j.id] {
		return nil
	}
	if j.final {
		delete(e.revisions, j.id)
		delete(e.finals, j.id)
	}
	if j.selection != nil {
		s := j.selection
		s.pending--
		if s.decided {
			return nil
		}
		body := StripLeadingPhrase(r.text, s.phrase)
		if strings.TrimSpace(body) == "" {
			if !s.recording && s.pending == 0 && e.gate == s {
				return e.cancelSelection("control_only")
			}
			return nil
		}
		s.decided = true
		if e.gate == s {
			s.recording = false
			e.gate = nil
			if err := e.recording(false, s.id); err != nil {
				return err
			}
		}
		return e.emit(Event{Type: "note_candidate", ID: s.utterance, SessionID: s.id, Revision: j.revision, Final: true, Span: j.span, Original: r.text, Body: body, Phrase: s.phrase})
	}
	return e.emit(Event{Type: "caption", ID: j.id, Revision: j.revision, Final: j.final, Span: j.span, Original: r.text})
}

func (e *Engine) cancelSelection(code string) error {
	if e.gate == nil {
		return nil
	}
	s := e.gate
	e.gate = nil
	s.decided = true
	s.recording = false
	return e.emit(Event{Type: "note_canceled", SessionID: s.id, Body: code})
}

// Stop flushes endpoints and drains eligible final results before joining ASR.
// A native inference stall requires the supervising parent to kill this process.
func (e *Engine) Stop() error {
	if e.closed {
		return nil
	}
	if err := e.authority.Err(); err != nil {
		e.Cancel()
		return err
	}
	e.closed = true
	a, err := e.front.Flush()
	if err == nil {
		err = e.activity(a)
	}
	e.front.Close()
	if err != nil {
		e.cancelWorker()
		return err
	}
	if e.gate != nil {
		id := e.gate.id
		e.gate.recording = false
		if err = e.recording(false, id); err != nil {
			e.cancelWorker()
			return err
		}
	}
	e.queue.close(false)
	for r := range e.results {
		if err = e.commitAuthority(); err != nil {
			e.cancelWorker()
			return err
		}
		if err = e.result(r); err != nil {
			e.cancelWorker()
			return err
		}
	}
	if err = e.commitAuthority(); err != nil {
		return err
	}
	return e.cancelSelection("shutdown")
}

func (e *Engine) commitAuthority() error {
	if e.stopCommit != nil {
		select {
		case <-e.authority.Done():
			return e.authority.Err()
		case <-e.stopCommit:
		}
	}
	return e.authority.Err()
}
func (e *Engine) cancelWorker() {
	close(e.abort)
	e.queue.close(true)
	for range e.results {
	}
}

// Cancel discards queued work and joins the worker; it never emits candidates.
func (e *Engine) Cancel() {
	if e.closed {
		return
	}
	e.closed = true
	e.front.Close()
	e.gate = nil
	e.cancelWorker()
}

// StripLeadingPhrase preserves exact original text separately. Only a known
// whole leading phrase is removed; no substring search or wake authorization.
func StripLeadingPhrase(original, phrase string) string {
	text := strings.TrimLeftFunc(original, unicode.IsSpace)
	if len(text) < len(phrase) || !strings.EqualFold(text[:len(phrase)], phrase) {
		return original
	}
	rest := text[len(phrase):]
	if rest != "" {
		r := []rune(rest)[0]
		if !unicode.IsSpace(r) && !unicode.IsPunct(r) {
			return original
		}
	}
	return strings.TrimLeftFunc(rest, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsPunct(r) })
}
