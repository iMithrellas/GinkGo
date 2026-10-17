package sherpavoice

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/mithrel/ginkgo/internal/voiceworker"
)

// Runtime injects native constructors and a single owned PCM recorder for tests.
type Runtime struct {
	Probe      func(string) (ProviderStatus, error)
	Open       func(NativeConfig) (Frontend, Recognizer, error)
	Microphone func(string, string) (PCMSource, error)
}

// NativeRuntime uses installed native models and the trusted recorder from init.
func NativeRuntime() Runtime {
	return Runtime{Probe: ProbeProvider, Open: OpenNative, Microphone: StartMicrophone}
}

type commandResult struct {
	command voiceworker.Command
	err     error
}

func commands(ctx context.Context, in io.Reader, ready <-chan struct{}) (<-chan commandResult, <-chan struct{}) {
	out := make(chan commandResult, 2)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer close(out)
		s := bufio.NewScanner(in)
		s.Split(func(data []byte, atEOF bool) (int, []byte, error) {
			for i, b := range data {
				if b == '\n' {
					return i + 1, data[:i], nil
				}
			}
			if atEOF && len(data) > 0 {
				return 0, nil, errors.New("unterminated command")
			}
			return 0, nil, nil
		})
		s.Buffer(make([]byte, 4096), voiceworker.MaxEventBytes+1)
		for s.Scan() {
			c, err := voiceworker.DecodeCommand(s.Bytes())
			if err == nil && c.Type == "start" {
				select {
				case <-ready:
				default:
					err = errors.New("start before ready")
				}
			}
			select {
			case out <- commandResult{c, err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
		err := s.Err()
		if err == nil {
			err = io.EOF
		}
		select {
		case out <- commandResult{err: err}:
		case <-ctx.Done():
		}
	}()
	return out, done
}

// eventWriter bounds output independently from capture. A blocked stdout can
// retain one writer goroutine only until process exit; drain has a deadline.
type eventWriter struct {
	authority context.Context
	queue     chan []byte
	done      chan struct{}
	err       error
	epoch     string
	sequence  uint64
	recording bool
	validator *voiceworker.Validator
}

func newEventWriter(out io.Writer, epoch string, c voiceworker.Config) *eventWriter {
	return newEventWriterContext(context.Background(), out, epoch, c)
}
func newEventWriterContext(ctx context.Context, out io.Writer, epoch string, c voiceworker.Config) *eventWriter {
	w := &eventWriter{queue: make(chan []byte, 8), done: make(chan struct{}), epoch: epoch, validator: voiceworker.NewValidator(epoch, c)}
	w.authority = ctx
	go func() {
		defer close(w.done)
		for b := range w.queue {
			if err := w.authority.Err(); err != nil {
				w.err = err
				return
			}
			if n, err := out.Write(b); err != nil || n != len(b) {
				w.err = errors.New("protocol output failed")
				return
			}
		}
	}()
	return w
}
func (w *eventWriter) send(e voiceworker.Event) error {
	if err := w.authority.Err(); err != nil {
		return err
	}
	e.Version = voiceworker.Version
	e.Epoch = w.epoch
	w.sequence++
	e.Sequence = w.sequence
	if e.Type == "wake" || e.Type == "state" || e.Type == "note_candidate" || e.Type == "note_canceled" {
		w.recording = e.RecordingNote
	} else {
		e.RecordingNote = w.recording
	}
	b, err := json.Marshal(e)
	if err != nil || len(b) > voiceworker.MaxEventBytes {
		return errors.New("protocol event too large")
	}
	if _, err = w.validator.Decode(b); err != nil {
		return errors.New("invalid helper event")
	}
	select {
	case <-w.done:
		return errors.New("protocol output ended")
	default:
	}
	select {
	case w.queue <- append(b, '\n'):
		return nil
	default:
		return errors.New("protocol output overflow")
	}
}
func (w *eventWriter) close() error {
	close(w.queue)
	t := time.NewTimer(time.Second)
	defer t.Stop()
	select {
	case <-w.done:
		return w.err
	case <-t.C:
		return errors.New("protocol output stalled")
	}
}

var recognitionMetadata = voiceworker.Metadata{Engine: "sherpa-onnx", Model: "parakeet-tdt-0.6b-v2", Language: "en"}

func (w *eventWriter) domain(e Event) error {
	span := voiceworker.Span{Start: e.Start, End: e.End}
	v := voiceworker.Event{Type: e.Type, RecordingNote: e.RecordingNote}
	switch e.Type {
	case "wake":
		v.SessionID = e.SessionID
		v.UtteranceID = e.ID
		v.Audio = &span
	case "recording":
		v.Type = "state"
		v.SessionID = e.SessionID
	case "activity":
		v.Type = "level"
		v.Level = &e.Level
	case "caption":
		v.Type = "transcript"
		v.Transcript = &voiceworker.Transcript{UtteranceID: e.ID, Revision: e.Revision, Final: e.Final, Text: e.Original, Audio: span, Metadata: recognitionMetadata}
	case "note_candidate":
		// KWS timestamps identify a start, not a precise control-prefix end.
		// Protocol v1 therefore conservatively preserves the trigger in Body.
		v.Candidate = &voiceworker.Candidate{SessionID: e.SessionID, UtteranceID: e.ID, Original: e.Original, Body: e.Original, Audio: span, Metadata: recognitionMetadata}
	case "note_canceled":
		v.SessionID = e.SessionID
		v.Code = e.Body
		if v.Code == "prefix_mismatch" || v.Code == "rearmed" {
			// These selections contain no accepted dictation. Keep protocol v1
			// cancellation codes unchanged; the internal cause is not a new ABI.
			v.Code = "empty"
		}
	default:
		return errors.New("invalid domain event")
	}
	return w.send(v)
}

func helperConfig(c voiceworker.Config) (EngineConfig, error) {
	e := DefaultEngineConfig()
	// An explicit helper-local bound supplements the broader wire contract.
	// Do not silently clamp requested final windows or allocate hour-long audio.
	ringMS := c.MaxRecordingMS + max(c.PreRollMS, c.WindowMS) + c.SilenceMS + 1000
	if c.MaxRecordingMS < 250 || c.WindowMS > 30000 || ringMS > 120000 {
		return e, errors.New("capture limits unsupported")
	}
	e.RingSamples = ringMS * 16
	e.PreviewSamples = c.WindowMS * 16
	e.PreviewEvery = c.UpdateMS * 16
	e.WakeTimeout = c.TriggerTimeoutMS * 16
	e.Cooldown = c.CooldownMS * 16
	e.PreRollSamples = c.PreRollMS * 16
	e.RecordingEnabled = c.RecordingEnabled
	e.SelectedKeyword = c.SelectedKeyword
	e.MaxRecordingSamples = c.MaxRecordingMS * 16
	return e, nil
}
func modelFiles(g voiceworker.Graph) ModelFiles {
	return ModelFiles{g.Encoder, g.Decoder, g.Joiner, g.Tokens}
}

// Serve implements only private voiceworker protocol v1 over inherited pipes.
// Init failure produces no stdout events, allowing one fresh CPU attempt. No
// microphone is opened before all models and the explicit start command. Serve
// owns in; its Close must unblock Read so the control reader can be joined.
func Serve(ctx context.Context, in io.ReadCloser, out io.Writer, runtime Runtime) error {
	ctx, cancel := context.WithCancel(ctx)
	ready := make(chan struct{})
	cmds, commandDone := commands(ctx, in, ready)
	defer func() { cancel(); _ = in.Close(); <-commandDone }()
	var init commandResult
	select {
	case <-ctx.Done():
		return ctx.Err()
	case init = <-cmds:
	}
	if init.err != nil || init.command.Type != "init" {
		return errors.New("invalid initialization")
	}
	c := *init.command.Config
	ec, err := helperConfig(c)
	if err != nil {
		return err
	}
	if runtime.Probe == nil || runtime.Open == nil || runtime.Microphone == nil {
		return errors.New("invalid runtime")
	}
	provider, err := runtime.Probe(string(c.ExecutionProvider()))
	if err != nil {
		return err
	}
	if provider.Actual == "cpu" && c.Provider != voiceworker.CPU && !c.AllowCPUFallback {
		return errors.New("provider unavailable")
	}
	if provider.Actual != "cpu" && provider.Actual != "cuda" {
		return errors.New("provider verification failed")
	}
	status := voiceworker.ProviderStatus{Requested: c.Provider, Effective: voiceworker.Provider(provider.Actual), FallbackReason: provider.FallbackCode}
	if c.AttemptProvider == voiceworker.CPU {
		status.FallbackReason = c.FallbackReason
	}
	nc := NativeConfig{RecordingEnabled: c.RecordingEnabled, ASR: modelFiles(c.Parakeet), KWS: modelFiles(c.KWS), Silero: c.SileroModel, KeywordFile: c.KeywordsFile, Provider: provider.Actual, Threads: 2, SilenceMS: c.SilenceMS, MaxSpeechMS: c.MaxRecordingMS}
	f, r, err := runtime.Open(nc)
	if err != nil {
		return err
	}
	w := newEventWriterContext(ctx, out, init.command.Epoch, c)
	e, err := NewEngine(ec, f, r, w.domain)
	if err != nil {
		f.Close()
		r.Close()
		_ = w.close()
		return err
	}
	defer e.Cancel()
	e.authority = ctx
	commit := make(chan struct{})
	e.stopCommit = commit
	stopping := make(chan struct{})
	e.stopping = stopping
	var stopMonitorDone chan struct{}
	stopBarrier := func() {
		close(stopping)
		stopMonitorDone = make(chan struct{})
		go func() {
			defer close(stopMonitorDone)
			select {
			case <-ctx.Done():
				return
			case v, ok := <-cmds:
				if ok && v.err == io.EOF {
					close(commit)
				} else {
					cancel()
				}
			}
		}()
	}
	defer func() {
		if stopMonitorDone != nil {
			cancel()
			<-stopMonitorDone
		}
	}()
	close(ready)
	if err = w.send(voiceworker.Event{Type: "ready", Provider: &status}); err != nil {
		_ = w.close()
		return err
	}
	finish := func(cause error) error {
		if ctx.Err() != nil {
			cause = ctx.Err()
		}
		if cause != nil {
			_ = w.send(voiceworker.Event{Type: "error", Code: "capture_failed"})
		} else {
			cause = w.send(voiceworker.Event{Type: "stopped"})
		}
		if closeErr := w.close(); cause == nil {
			cause = closeErr
		}
		return cause
	}
	check := func(v commandResult) bool {
		return v.err == nil && v.command.Epoch == init.command.Epoch && v.command.Type != "init"
	}
	var start commandResult
	select {
	case <-ctx.Done():
		return finish(ctx.Err())
	case start = <-cmds:
	}
	if !check(start) {
		return finish(errors.New("invalid control command"))
	}
	if start.command.Type == "stop" {
		stopBarrier()
		return finish(e.Stop())
	}
	if start.command.Type != "start" {
		return finish(errors.New("invalid control command"))
	}
	source, err := runtime.Microphone(c.RecorderExecutable, c.RecorderTarget)
	if err != nil {
		return finish(err)
	}
	stop := make(chan struct{})
	captureDone := make(chan error, 1)
	go func() { captureDone <- Capture(ctx, stop, source, e) }()
	select {
	case err = <-captureDone:
		return finish(err)
	case <-ctx.Done():
		cancel()
		<-captureDone
		return finish(ctx.Err())
	case command := <-cmds:
		if !check(command) || command.command.Type != "stop" {
			cancel()
			<-captureDone
			return finish(errors.New("invalid control command"))
		}
		stopBarrier()
		close(stop)
		err = <-captureDone
		return finish(err)
	}
}
