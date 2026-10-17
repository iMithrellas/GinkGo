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
	return EngineConfig{RingSamples: 35 * SampleRate, PreviewSamples: 5 * SampleRate, PreviewEvery: SampleRate / 2, MinSpeech: SampleRate / 4, WakeTimeout: 3 * SampleRate, Cooldown: 2 * SampleRate, FinalQueue: 8, PreRollSamples: SampleRate / 2, RecordingEnabled: true, MaxRecordingSamples: 30 * SampleRate}
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
	active           bool
	label            string
	closingEnd       int64
	endedCode        string
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
	deferred                               *selection // one bounded unpublished retrigger
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
		if !e.config.RecordingEnabled || e.config.SelectedKeyword != "" && !strings.EqualFold(w.Phrase, e.config.SelectedKeyword) || e.ring.end < e.cooldownUntil {
			continue
		}
		// The grid is evidence, not a physical phonetic onset. Keep configured
		// source context intact and reject eviction rather than silently clipping.
		start := max(int64(0), w.Start-int64(e.config.PreRollSamples))
		if start < e.ring.start() {
			continue
		}
		noteID := e.id("note")
		s := &selection{id: noteID, utterance: noteID + "-audio", phrase: w.SpokenForm, label: w.Phrase, start: start, wakeEnd: w.End, deadline: w.End + int64(e.config.WakeTimeout), recording: true}
		e.cooldownUntil = e.ring.end + int64(e.config.Cooldown)
		if err := e.retrigger(s); err != nil {
			return err
		}
	}
	// Idle arming is an AUDIO-clock bound, never extended by ASR latency or
	// Silero hangover. Continuous speech that began in time may finish later.
	if s := e.capturing(); s != nil && s.recording && !s.active && e.ring.end-int64(len(p)) >= s.deadline {
		if err := e.endSelection(s, "timeout"); err != nil {
			return err
		}
	}
	if s := e.capturing(); s != nil && s.recording && a.Speaking {
		s.active = true
		s.hasSpeech = true
	}
	if err := e.activity(a); err != nil {
		return err
	}
	if s := e.capturing(); s != nil && !a.Speaking {
		// A VAD pulse can disappear without a qualifying completed segment.
		// It must not leave the session active indefinitely.
		s.active = false
	}
	if s := e.capturing(); s != nil && s.recording && e.ring.end-s.start >= int64(e.config.MaxRecordingSamples) {
		if s.hasSpeech {
			if err := e.selectFinal(s, s.start+int64(e.config.MaxRecordingSamples)); err != nil {
				return err
			}
		}
		if err := e.endSelection(s, "empty"); err != nil {
			return err
		}
	}
	if s := e.capturing(); s != nil && s.recording && !s.active && e.ring.end >= s.deadline {
		if err := e.endSelection(s, "timeout"); err != nil {
			return err
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

// capturing can be an unpublished selection while the public singleton finishes
// ASR. PCM, endpoints and its idle deadline continue on the same source clock.
func (e *Engine) capturing() *selection {
	if e.deferred != nil {
		return e.deferred
	}
	return e.gate
}

func (e *Engine) announce(s *selection) error {
	e.gate = s
	if err := e.emit(Event{Type: "wake", ID: s.utterance, SessionID: s.id, Phrase: s.label, Span: Span{s.start, s.wakeEnd}, RecordingNote: true}); err != nil {
		return err
	}
	if err := e.recording(true, s.id); err != nil {
		return err
	}
	if !s.recording {
		if err := e.recording(false, s.id); err != nil {
			return err
		}
		if s.pending == 0 {
			code := s.endedCode
			if code == "" {
				code = "empty"
			}
			return e.cancelSelection(code)
		}
	}
	return nil
}

func (e *Engine) promote() error {
	if e.gate != nil || e.deferred == nil {
		return nil
	}
	s := e.deferred
	e.deferred = nil
	return e.announce(s)
}

func (e *Engine) retrigger(next *selection) error {
	if e.deferred != nil {
		return errors.New("deferred acoustic wake overflow")
	}
	old := e.gate
	if old == nil {
		return e.announce(next)
	}
	e.deferred = next
	// Do not wait for a VAD endpoint: its hangover may merge both wakes.
	// Exclude ALL new retained context, not a guessed phonetic word end.
	end := min(next.start, old.start+int64(e.config.MaxRecordingSamples))
	if !old.recording && old.lastEnd > 0 {
		end = min(end, old.lastEnd)
	}
	if !old.hasSpeech || end < old.wakeEnd || end-old.start < int64(e.config.MinSpeech) {
		return e.cancelSelection("rearmed")
	}
	old.closingEnd = end
	if old.lastEnd != end || old.pending == 0 {
		if err := e.submit(e.id("selected"), Span{old.start, end}, true, old); err != nil {
			return err
		}
		old.pending++
		old.lastEnd = end
	}
	wasRecording := old.recording
	old.recording = false
	if wasRecording {
		return e.recording(false, old.id)
	}
	return nil
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
		if s := e.capturing(); s != nil && s.recording && !s.decided && span.End >= s.wakeEnd {
			if !s.active && span.Start >= s.deadline {
				continue
			}
			s.hasSpeech = true
			firstEndpoint := s.lastEnd == 0
			if err := e.selectFinal(s, min(span.End, s.start+int64(e.config.MaxRecordingSamples))); err != nil {
				return err
			}
			s.active = false
			if firstEndpoint {
				s.deadline = span.End + int64(e.config.WakeTimeout)
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
func (e *Engine) selectFinal(s *selection, end int64) error {
	if end < s.wakeEnd || end <= s.lastEnd || end-s.start < int64(e.config.MinSpeech) {
		return nil
	}
	// Keep the source-exact conservative wake context and a permitted pause.
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
		if s != e.gate {
			return errors.New("selected result before wake authority")
		}
		// A retrigger supersedes any earlier endpoint snapshot, particularly a
		// merged endpoint that included the NEXT wake. Only the bounded close
		// snapshot is authoritative for this old session.
		if s.closingEnd != 0 && j.span.End != s.closingEnd {
			return nil
		}
		body, leading := selectedBody(r.text, s.phrase)
		if !leading {
			if e.gate == s {
				return e.cancelSelection("prefix_mismatch")
			}
			s.decided = true
			return nil
		}
		if body == "" {
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
		if err := e.emit(Event{Type: "note_candidate", ID: s.utterance, SessionID: s.id, Revision: j.revision, Final: true, Span: j.span, Original: r.text, Body: body, Phrase: s.phrase}); err != nil {
			return err
		}
		return e.promote()
	}
	return e.emit(Event{Type: "caption", ID: j.id, Revision: j.revision, Final: j.final, Span: j.span, Original: r.text})
}

func (e *Engine) endSelection(s *selection, code string) error {
	if s == nil {
		return nil
	}
	s.recording = false
	s.endedCode = code
	if s == e.gate {
		if err := e.recording(false, s.id); err != nil {
			return err
		}
		if s.pending == 0 {
			return e.cancelSelection(code)
		}
	}
	return nil
}

func (e *Engine) cancelSelection(code string) error {
	if e.gate == nil {
		return nil
	}
	s := e.gate
	e.gate = nil
	s.decided = true
	s.recording = false
	if err := e.emit(Event{Type: "note_canceled", SessionID: s.id, Body: code}); err != nil {
		return err
	}
	return e.promote()
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
	if s := e.capturing(); s != nil {
		if err = e.endSelection(s, "shutdown"); err != nil {
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
	for e.gate != nil {
		if err := e.cancelSelection("shutdown"); err != nil {
			return err
		}
	}
	return nil
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
	e.deferred = nil
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
