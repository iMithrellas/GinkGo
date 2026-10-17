package daemon

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/mithrel/ginkgo/internal/config"
	"github.com/mithrel/ginkgo/internal/ipc"
	"github.com/mithrel/ginkgo/internal/notes"
	"github.com/mithrel/ginkgo/internal/voice"
	"github.com/mithrel/ginkgo/internal/voiceevents"
	"github.com/mithrel/ginkgo/pkg/api"
)

type liveCapturer interface {
	CaptureLive(context.Context, voice.LiveCaptureOptions, <-chan struct{}, func(voice.LiveCaptureEvent) error) error
}

type residentSTT interface {
	Transcribe(context.Context, []byte, voice.Options) (voice.Transcript, error)
	Close() error
}

func (v *voiceRuntime) stream(ctx context.Context, m ipc.Message) ipc.Response {
	if !v.enabled {
		return ipc.Response{Msg: voice.ErrDisabled.Error()}
	}
	if !v.streamEnabled || v.events == nil {
		return ipc.Response{Msg: "voice stream requires voice.stream.enabled and voice.events.enabled"}
	}
	if v.streamErr != nil {
		return ipc.Response{Msg: v.streamErr.Error()}
	}
	if !v.residentConfig.AllowLocalHTTP {
		return ipc.Response{Msg: "voice stream requires explicit voice.stream.allow_local_http consent; see resident security documentation"}
	}
	if m.Mode != "" && m.Mode != "raw" || m.Provider != "" || m.NoteID != "" || m.Action != "" {
		return ipc.Response{Msg: "voice stream supports raw notes only; AI note actions are not supported"}
	}
	if m.Trigger == "" {
		m.Trigger = v.triggerPhrase
	}
	if err := config.ValidateTriggerPhrase(m.Trigger); err != nil {
		return ipc.Response{Msg: err.Error()}
	}
	if m.Save && strings.TrimSpace(m.Trigger) == "" {
		return ipc.Response{Msg: "stream saving requires a literal transcript-prefix trigger; use voice enable for all-utterance saving"}
	}
	m.Mode = "raw"
	o, err := v.listenOptions(m, true)
	if err != nil {
		return ipc.Response{Msg: err.Error()}
	}
	if (v.streamUpdate != 0 && (v.streamUpdate < 10*time.Millisecond || v.streamUpdate > 5*time.Second)) || (v.streamWindow != 0 && (v.streamWindow < 150*time.Millisecond || v.streamWindow > 30*time.Second)) {
		return ipc.Response{Msg: "invalid live update interval or window"}
	}
	if strings.TrimSpace(v.residentConfig.Executable) == "" {
		return ipc.Response{Msg: "voice.stream.executable is required"}
	}
	if err := ctx.Err(); err != nil {
		return ipc.Response{Msg: err.Error()}
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed {
		return ipc.Response{Msg: "voice daemon is shutting down"}
	}
	if v.job != nil {
		return ipc.Response{Msg: "voice capture is busy"}
	}
	parent := v.root
	if parent == nil {
		parent = context.Background()
	}
	if err := parent.Err(); err != nil {
		return ipc.Response{Msg: err.Error()}
	}
	jobCtx, cancel := context.WithCancel(parent)
	j := &listenJob{streamID: api.NewID(), cancel: cancel, stop: make(chan struct{}), done: make(chan struct{})}
	m.Deadline = time.Time{}
	m.Tags = append([]string(nil), m.Tags...)
	v.job = j
	v.status = ipc.VoiceStatus{Enabled: true, Active: true, Continuous: true, State: "streaming", Namespace: m.Namespace, SilenceTimeout: o.SilenceTimeout, StartedAt: time.Now().UTC()}
	go v.runStream(jobCtx, j, m, voice.LiveCaptureOptions{CaptureOptions: o, UpdateInterval: v.streamUpdate, WindowDuration: v.streamWindow})
	s := v.status
	return ipc.Response{OK: true, VoiceStatus: &s}
}

// streamQueue bounds retained audio to four final utterances and one latest
// partial, plus single in-flight inference. It never blocks the recorder.
type streamQueue struct {
	mu        sync.Mutex
	finals    []voice.LiveCaptureEvent
	partial   *voice.LiveCaptureEvent
	finalized uint64
	wake      chan struct{}
	finished  bool
	fault     error
	gate      streamGate
	hub       *voiceevents.Hub
	cancel    context.CancelFunc
}

func (s *streamQueue) fail(err error) {
	if err != nil && s.fault == nil {
		s.fault = err
		s.cancel()
	}
}
func (s *streamQueue) publish(e voiceevents.Event) { s.fail(s.hub.Publish(e)) }
func (s *streamQueue) state(listening bool) {
	s.fail(s.hub.UpdateState(voiceevents.State{Listening: listening, RecordingNote: s.gate.id != "", ActiveNoteSessionID: s.gate.id}))
}
func (s *streamQueue) observe(now time.Time, utterance uint64, qualified bool) {
	previous := s.gate.utterance
	id := s.gate.observe(now, utterance, qualified)
	if id != "" || previous != s.gate.utterance {
		s.state(true)
	}
	if id != "" {
		s.publish(voiceevents.Event{Type: "note.canceled", NoteSessionID: id})
	}
}
func (s *streamQueue) tick(now time.Time) { s.observe(now, 0, false) }
func (s *streamQueue) enqueue(e voice.LiveCaptureEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fault != nil {
		return s.fault
	}
	// Capture emits partial/final only after 150ms qualifying speech. Activity
	// can describe shorter discarded noise: observe its onset without selecting it.
	s.observe(time.Now(), e.UtteranceID, e.Kind == "partial" || e.Kind == "final")
	s.publish(voiceevents.Event{Type: "level", Level: &e.Level, Speech: &e.Speech})
	if e.Kind == "activity" {
		return s.fault
	}
	if len(e.WAV) > 32<<20 {
		s.fail(errors.New("live audio exceeds byte limit"))
		return s.fault
	}
	if e.Kind == "final" {
		if e.Reason == "device_lost" || e.Reason == "stall" {
			s.fail(errors.New("voice capture failed; device lost or stalled"))
			return s.fault
		}
		if e.UtteranceID <= s.finalized {
			return nil
		}
		if len(s.finals) >= captureQueueSize {
			s.fail(errCaptureOverload)
			return s.fault
		}
		s.finalized = e.UtteranceID
		if s.partial != nil && s.partial.UtteranceID <= e.UtteranceID {
			s.partial = nil
		}
		s.finals = append(s.finals, e)
	} else if e.Kind == "partial" && e.UtteranceID > s.finalized {
		s.partial = &e
	}
	select {
	case s.wake <- struct{}{}:
	default:
	}
	return s.fault
}

func (s *streamQueue) next() (voice.LiveCaptureEvent, bool, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fault != nil {
		return voice.LiveCaptureEvent{}, false, true
	}
	if len(s.finals) > 0 {
		e := s.finals[0]
		s.finals[0] = voice.LiveCaptureEvent{}
		s.finals = s.finals[1:]
		return e, true, false
	}
	if s.partial != nil {
		e := *s.partial
		s.partial = nil
		return e, true, false
	}
	return voice.LiveCaptureEvent{}, false, s.finished
}

func (v *voiceRuntime) runStream(ctx context.Context, j *listenJob, m ipc.Message, o voice.LiveCaptureOptions) {
	defer j.cancel()
	s := &streamQueue{wake: make(chan struct{}, 1), gate: streamGate{enabled: m.Save, phrase: m.Trigger}, hub: v.events, cancel: j.cancel}
	s.state(true)
	c := v.residentConfig
	if m.Model != "" {
		c.Model = m.Model
	}
	factory := v.residentFactory
	if factory == nil {
		factory = func(c voice.ResidentConfig) residentSTT { return voice.NewResident(c) }
	}
	resident := factory(c)
	captureDone := make(chan struct{})
	go func() {
		defer close(captureDone)
		err := v.liveCapturer.CaptureLive(ctx, o, j.stop, s.enqueue)
		s.mu.Lock()
		if err != nil && ctx.Err() == nil {
			s.fail(errors.New("voice capture failed; check recorder and audio device"))
		}
		s.finished = true
		s.mu.Unlock()
		select {
		case s.wake <- struct{}{}:
		default:
		}
	}()
	// Timeout remains bounded even while inference is busy. Joined below.
	tickDone, tickStop := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(tickDone)
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tickStop:
				return
			case now := <-ticker.C:
				s.mu.Lock()
				s.tick(now)
				s.mu.Unlock()
			}
		}
	}()
	for ctx.Err() == nil {
		e, ok, done := s.next()
		if done {
			break
		}
		if !ok {
			select {
			case <-ctx.Done():
			case <-s.wake:
			}
			continue
		}
		tr, err := resident.Transcribe(ctx, e.WAV, voice.Options{Language: m.Language})
		if err != nil {
			s.mu.Lock()
			if ctx.Err() == nil {
				s.fail(errors.New("live transcription failed; check installed whisper-server and model"))
			}
			s.mu.Unlock()
			break
		}
		if ctx.Err() != nil {
			break
		}
		s.mu.Lock()
		id := ""
		if s.gate.utterance == e.UtteranceID {
			id = s.gate.id
		}
		body, original := "", ""
		previousNote := s.gate.id
		if e.Kind == "final" {
			body, original, id = s.gate.final(time.Now(), e.UtteranceID, tr.Text)
			// Same-breath activation is only known after final inference. State
			// precedes final publication/persistence, never claims acoustic timing.
			if id != "" {
				s.fail(s.hub.UpdateState(voiceevents.State{Listening: true, RecordingNote: true, ActiveNoteSessionID: id}))
			} else {
				s.state(true)
			}
		}
		start, end := (e.StartSample-e.UtteranceStartSample)/16, (e.EndSample-e.UtteranceStartSample)/16
		attribution := id
		// Only the final that creates an arm inherits its new session. An excluded
		// ambient final must not borrow attribution merely because an arm is waiting.
		if attribution == "" && e.Kind == "final" && s.gate.id != "" && s.gate.id != previousNote {
			attribution = s.gate.id
		}
		s.publish(voiceevents.Event{Type: "transcript", UtteranceID: fmt.Sprintf("%s/%d", j.streamID, e.UtteranceID), Revision: e.Revision, Text: tr.Text, Final: e.Kind == "final", NoteSessionID: attribution, AudioStartMS: &start, AudioEndMS: &end})
		if e.Kind == "final" && id != "" {
			s.state(true)
		}
		s.mu.Unlock()
		if ctx.Err() != nil {
			if e.Kind == "final" && id != "" {
				s.mu.Lock()
				s.publish(voiceevents.Event{Type: "note.canceled", NoteSessionID: id})
				s.mu.Unlock()
			}
			break
		}
		if e.Kind == "final" {
			v.mu.Lock()
			v.status.Segments++
			v.mu.Unlock()
			if id != "" {
				outcome := voiceevents.Event{Type: "note.canceled", NoteSessionID: id}
				if strings.TrimSpace(body) != "" {
					entry, err := v.saveStreamNote(ctx, m, tr, body, original)
					if err != nil {
						if ctx.Err() == nil {
							outcome.Type, outcome.Error = "note.error", "voice note save failed"
						}
					} else {
						outcome.Type, outcome.NoteID = "note.saved", entry.ID
						v.mu.Lock()
						v.status.LastNoteID = entry.ID
						v.mu.Unlock()
						v.scheduleSync()
					}
				}
				s.mu.Lock()
				s.publish(outcome)
				s.mu.Unlock()
			}
		}
	}
	<-captureDone
	close(tickStop)
	<-tickDone
	closeErr := resident.Close()
	s.mu.Lock()
	if closeErr != nil && s.fault == nil {
		s.fault = errors.New("close resident speech engine failed")
	}
	canceledID := s.gate.clear(time.Now())
	s.finals, s.partial = nil, nil
	s.state(false)
	if canceledID != "" {
		s.publish(voiceevents.Event{Type: "note.canceled", NoteSessionID: canceledID})
	}
	fault := s.fault
	if fault != nil {
		s.publish(voiceevents.Event{Type: "error", Error: fault.Error()})
	}
	s.mu.Unlock()
	v.mu.Lock()
	v.status.Active, v.status.Continuous, v.status.State = false, false, "idle"
	result := ipc.Response{OK: true}
	if fault != nil {
		v.status.State, v.status.LastError = "error", fault.Error()
		result.OK, result.Msg = false, fault.Error()
	} else if ctx.Err() != nil {
		v.status.State = "canceled"
	}
	status := v.status
	result.VoiceStatus = &status
	j.result, v.job = result, nil
	close(j.done)
	v.mu.Unlock()
}

func (v *voiceRuntime) saveStreamNote(ctx context.Context, m ipc.Message, tr voice.Transcript, body, original string) (api.Entry, error) {
	tags := m.Tags
	if len(tags) == 0 {
		tags = v.defaultTags
	}
	title := m.Title
	if strings.TrimSpace(title) == "" {
		title = notes.RawTitle(body)
	}
	return v.notes.Create(ctx, notes.CreateRequest{Namespace: m.Namespace, Title: title, Body: body, OriginalText: original, Tags: tags, Source: &api.Source{Kind: "voice", Language: tr.Language, STTEngine: tr.Engine, STTModel: tr.Model}})
}
