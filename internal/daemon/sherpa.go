package daemon

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/mithrel/ginkgo/internal/ai"
	"github.com/mithrel/ginkgo/internal/config"
	"github.com/mithrel/ginkgo/internal/ipc"
	"github.com/mithrel/ginkgo/internal/voice"
	"github.com/mithrel/ginkgo/internal/voiceassets"
	"github.com/mithrel/ginkgo/internal/voiceevents"
	"github.com/mithrel/ginkgo/internal/voiceworker"
	"github.com/mithrel/ginkgo/pkg/api"
)

type workerRunner interface {
	Run(context.Context, voiceworker.Config, <-chan struct{}, func(voiceworker.Event) error) error
}

func installedExecutable(name string) (string, error) {
	path, err := voiceassets.Executable(name)
	if err != nil {
		return "", errors.New("installed voice executable not found; run voice setup or check executable overrides")
	}
	return path, nil
}

func (v *voiceRuntime) startSherpa(ctx context.Context, m ipc.Message) ipc.Response {
	if m.NoteID != "" || m.Action != "" || m.AudioPath != "" || m.Model != "" || m.Language != "" && m.Language != "en" || m.Capture != nil {
		return ipc.Response{Msg: "sherpa stream supports configured English new notes only; no legacy STT or capture overrides"}
	}
	if len(m.Namespace) > 256 || strings.ContainsRune(m.Namespace, 0) {
		return ipc.Response{Msg: "invalid voice namespace"}
	}
	s := v.sherpaSettings
	if s == nil {
		return ipc.Response{Msg: "missing sherpa configuration"}
	}
	if m.Mode == "" {
		m.Mode = s.GetString("voice.mode")
	}
	if m.Mode == "" {
		m.Mode = "raw"
	}
	if m.Mode != "raw" && m.Mode != "ai" {
		return ipc.Response{Msg: "voice mode must be raw or ai"}
	}
	if m.Provider != "" && (!m.Save || m.Mode != "ai") {
		return ipc.Response{Msg: "AI provider override requires save and AI mode"}
	}
	if m.Trigger == "" {
		m.Trigger = s.GetString("voice.sherpa.keyword")
	}
	if err := ctx.Err(); err != nil {
		return ipc.Response{Msg: err.Error()}
	}
	// Only an explicit enabled stream request resolves executables. No constructor,
	// status, watcher, disable, or ordinary Whisper request reaches this code.
	recorder, err := installedExecutable(s.GetString("voice.capture.executable"))
	if err != nil {
		return ipc.Response{Msg: err.Error()}
	}
	c, err := config.SherpaConfig(s, recorder, s.GetString("voice.capture.target"), m.Trigger, m.Save)
	if err != nil {
		return ipc.Response{Msg: err.Error()}
	}
	helper, err := installedExecutable(s.GetString("voice.sherpa.executable"))
	if err != nil {
		return ipc.Response{Msg: err.Error()}
	}
	clientConfig, err := config.SherpaClientConfig(s, helper)
	if err != nil {
		return ipc.Response{Msg: err.Error()}
	}
	runner := v.sherpaRunner
	if runner == nil {
		runner = voiceworker.New(clientConfig)
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
	v.status = ipc.VoiceStatus{Enabled: true, Active: true, Continuous: true, State: "starting", Namespace: m.Namespace, StartedAt: time.Now().UTC(), SilenceTimeout: time.Duration(c.SilenceMS) * time.Millisecond, Backend: "sherpa", RequestedProvider: string(c.Provider), EffectiveProvider: "unknown"}
	go v.runSherpa(jobCtx, j, m, c, runner)
	status := v.status
	return ipc.Response{OK: true, VoiceStatus: &status}
}

// sherpaIngest is a bounded, sealable callback gate. It never stores notes, runs
// AI, synchronizes, or does socket/disk I/O. Run may return while its last callback
// is still scheduled: seal joins a callback already inside this gate, and every
// later invocation is inert. The job owns the gate until persistence is joined.
type sherpaIngest struct {
	mu       sync.Mutex
	sealed   bool
	fault    bool
	selected string
	queue    []voiceworker.Candidate
	pending  map[string]bool
	wake     chan struct{}
	v        *voiceRuntime
	j        *listenJob
	ctx      context.Context
	save     bool
}

const sherpaStreamError = "voice helper stream failed; check installed helper, models, and recorder"

func (s *sherpaIngest) id(id string) string { return s.j.streamID + "/" + id }
func (s *sherpaIngest) fail() error {
	if !s.fault {
		_ = s.v.events.UpdateState(voiceevents.State{})
		_ = s.v.events.Publish(voiceevents.Event{Type: "error", Error: sherpaStreamError})
		s.v.mu.Lock()
		s.v.status.State, s.v.status.LastError = "error", sherpaStreamError
		s.v.mu.Unlock()
	}
	s.fault = true
	s.queue = nil
	s.j.cancel()
	return errors.New("voice helper stream failed")
}
func (s *sherpaIngest) state(recording bool) error {
	id := ""
	if recording {
		id = s.id(s.selected)
	}
	return s.v.events.UpdateState(voiceevents.State{Listening: true, RecordingNote: recording, ActiveNoteSessionID: id})
}
func (s *sherpaIngest) emit(e voiceworker.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sealed || s.ctx.Err() != nil {
		return context.Canceled
	}
	var err error
	switch e.Type {
	case "ready":
		if e.Provider == nil {
			return s.fail()
		}
		s.v.mu.Lock()
		s.v.status.State, s.v.status.HelperEngine = "streaming", "sherpa-onnx"
		s.v.status.RequestedProvider, s.v.status.EffectiveProvider, s.v.status.ProviderFallbackReason = string(e.Provider.Requested), string(e.Provider.Effective), e.Provider.FallbackReason
		s.v.mu.Unlock()
		err = s.state(false)
	case "wake":
		if !s.save || s.selected != "" || e.SessionID == "" || s.pending[e.SessionID] {
			return s.fail()
		}
		s.selected = e.SessionID
		s.pending[e.SessionID] = true
		err = s.state(true)
	case "state":
		if e.RecordingNote && (!s.save || s.selected == "" || e.SessionID != s.selected) {
			return s.fail()
		}
		err = s.state(e.RecordingNote)
	case "level":
		err = s.v.events.Publish(voiceevents.Event{Type: "level", Level: e.Level})
	case "transcript":
		if e.Transcript == nil {
			return s.fail()
		}
		t := e.Transcript
		start, end := t.Audio.Start/16, t.Audio.End/16
		err = s.v.events.Publish(voiceevents.Event{Type: "transcript", UtteranceID: s.id(t.UtteranceID), Revision: t.Revision, Final: t.Final, Text: t.Text, AudioStartMS: &start, AudioEndMS: &end})
	case "note_candidate":
		if !s.save || e.Candidate == nil || s.selected == "" || e.Candidate.SessionID != s.selected || strings.TrimSpace(e.Candidate.Body) == "" || len(s.queue) >= captureQueueSize {
			return s.fail()
		}
		s.selected = ""
		s.queue = append(s.queue, *e.Candidate)
		err = s.state(false)
		if err == nil {
			// Selected ASR is a distinct utterance from ambient captions. Publish
			// its sole exact final for observers; only the private candidate gate
			// above, never this public transcript, authorizes persistence.
			c := e.Candidate
			start, end := c.Audio.Start/16, c.Audio.End/16
			err = s.v.events.Publish(voiceevents.Event{Type: "transcript", UtteranceID: s.id(c.UtteranceID), Revision: 1, Final: true, Text: c.Original, NoteSessionID: s.id(c.SessionID), AudioStartMS: &start, AudioEndMS: &end})
		}
		select {
		case s.wake <- struct{}{}:
		default:
		}
	case "note_canceled":
		if e.SessionID != s.selected || s.selected == "" {
			return s.fail()
		}
		s.selected = ""
		delete(s.pending, e.SessionID)
		err = s.state(false)
		if err == nil {
			err = s.v.events.Publish(voiceevents.Event{Type: "note.canceled", NoteSessionID: s.id(e.SessionID)})
		}
	case "error":
		return s.fail() // Never expose raw native errors, stderr, or model paths.
	case "stopped":
		err = s.state(false)
	default:
		return s.fail()
	}
	if err != nil {
		return s.fail()
	}
	return nil
}

func (v *voiceRuntime) runSherpa(ctx context.Context, j *listenJob, m ipc.Message, c voiceworker.Config, runner workerRunner) {
	defer j.cancel()
	s := &sherpaIngest{v: v, j: j, ctx: ctx, save: m.Save, pending: make(map[string]bool), wake: make(chan struct{}, 1)}
	runDone := make(chan error, 1)
	go func() {
		err := runner.Run(ctx, c, j.stop, s.emit)
		s.mu.Lock() // Join any callback in the gate, reject all late callbacks.
		s.sealed = true
		if err != nil && ctx.Err() == nil {
			s.fail()
		}
		s.mu.Unlock()
		runDone <- err
	}()
	finished := false
	for {
		s.mu.Lock()
		var candidate *voiceworker.Candidate
		if !s.fault && ctx.Err() == nil && len(s.queue) > 0 {
			next := s.queue[0]
			s.queue[0] = voiceworker.Candidate{}
			s.queue = s.queue[1:]
			candidate = &next
		}
		s.mu.Unlock()
		if candidate != nil {
			// This goroutine is the explicitly owned persistence worker. It is
			// joined before done closes, including an uncooperative storage call.
			outcome := voiceevents.Event{Type: "note.canceled", NoteSessionID: s.id(candidate.SessionID)}
			if ctx.Err() == nil {
				// Rebind provenance from the sole supported trusted local model
				// contract; no worker-provided metadata or ambient text is saved.
				tr := voice.Transcript{Engine: "sherpa-onnx", Model: "parakeet-tdt-0.6b-v2", Language: "en"}
				result, err := v.saveSherpaNote(ctx, m, tr, candidate.Body, candidate.Original)
				if err == nil {
					entry := result.Entry
					outcome.Type, outcome.NoteID = "note.saved", entry.ID
					outcome.Processing, outcome.FallbackReason = result.processing, result.FallbackReason
					v.mu.Lock()
					v.status.LastNoteID, v.status.Segments = entry.ID, v.status.Segments+1
					v.mu.Unlock()
					v.scheduleSync()
				} else if ctx.Err() == nil {
					outcome.Type, outcome.Error = "note.error", "voice note save failed"
				}
			}
			s.mu.Lock()
			delete(s.pending, candidate.SessionID)
			_ = v.events.Publish(outcome)
			s.mu.Unlock()
			continue
		}
		if finished {
			break
		}
		select {
		case <-s.wake:
		case <-runDone:
			finished = true
		}
	}
	s.mu.Lock()
	s.queue = nil
	_ = v.events.UpdateState(voiceevents.State{})
	for id := range s.pending {
		_ = v.events.Publish(voiceevents.Event{Type: "note.canceled", NoteSessionID: s.id(id)})
	}
	fault := s.fault
	s.mu.Unlock()
	v.mu.Lock()
	v.status.Active, v.status.Continuous, v.status.State = false, false, "idle"
	result := ipc.Response{OK: true}
	if fault {
		v.status.State, v.status.LastError = "error", sherpaStreamError
		result.OK, result.Msg = false, v.status.LastError
	} else if ctx.Err() != nil {
		v.status.State = "canceled"
	}
	status := v.status
	result.VoiceStatus = &status
	j.result, v.job = result, nil
	close(j.done)
	v.mu.Unlock()
}

type sherpaNoteResult struct {
	ai.NoteResult
	processing string
}

// saveSherpaNote runs only in the joined persistence worker, never the capture
// callback. AI owns the sole write (including raw fallback); its Text must be the
// exact full ASR original because Source.OriginalText is not used by ai.Service.
// The shared Whisper/raw saver deliberately retains its existing behavior.
func (v *voiceRuntime) saveSherpaNote(ctx context.Context, m ipc.Message, tr voice.Transcript, body, original string) (sherpaNoteResult, error) {
	if v.persistSherpa != nil {
		entry, err := v.persistSherpa(ctx, m, tr, body, original)
		return sherpaNoteResult{NoteResult: ai.NoteResult{Entry: entry}}, err
	}
	if m.Mode != "ai" {
		entry, err := v.saveStreamNote(ctx, m, tr, body, original)
		return sherpaNoteResult{NoteResult: ai.NoteResult{Entry: entry}}, err
	}
	if v.ai == nil {
		return sherpaNoteResult{}, errors.New("missing AI note service")
	}
	result, err := v.ai.Note(ctx, ai.NoteRequest{
		Text: original, Namespace: m.Namespace, Title: m.Title, Tags: m.Tags, Action: "create",
		Source: &api.Source{Kind: "voice", Language: tr.Language, STTEngine: tr.Engine, STTModel: tr.Model},
	}, m.Provider)
	processing := "ai"
	if result.FallbackReason != "" {
		// Service reasons are application-owned diagnostics, never provider error
		// strings, credentials, capture paths, or model-generated content.
		processing = "raw_fallback"
	}
	return sherpaNoteResult{NoteResult: result, processing: processing}, err
}
