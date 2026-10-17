package daemon

import (
	"context"
	"errors"
	"math"
	"os"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/spf13/viper"

	"github.com/mithrel/ginkgo/internal/ipc"
	"github.com/mithrel/ginkgo/internal/voice"
)

const captureQueueSize = 4

var errCaptureOverload = errors.New("voice capture queue overloaded; listening stopped")

// segmentCapturer makes stream ownership and cancellation independently testable.
// Capture must join its device/process workers before returning. The daemon
// snapshots each emitted WAV so a recorder can reuse its buffer after emit.
type segmentCapturer interface {
	Capture(context.Context, voice.CaptureOptions, <-chan struct{}, func(voice.Segment) error) error
}

type listenJob struct {
	streamID string
	cancel   context.CancelFunc
	stop     chan struct{}
	stopping bool
	done     chan struct{}
	result   ipc.Response // written before closing done
}

func captureSettings(v *viper.Viper, enabled bool) (voice.CaptureConfig, error) {
	c := voice.CaptureConfig{Enabled: enabled, Executable: "pw-record", Target: v.GetString("voice.capture.target"), SilenceTimeout: 1500 * time.Millisecond, MaxDuration: 2 * time.Minute, StartTimeout: 30 * time.Second, Threshold: .02}
	if v.IsSet("voice.capture.executable") {
		c.Executable = v.GetString("voice.capture.executable")
	}
	for key, dest := range map[string]*time.Duration{"silence": &c.SilenceTimeout, "max_duration": &c.MaxDuration, "start_timeout": &c.StartTimeout} {
		if !v.IsSet("voice.capture." + key) {
			continue
		}
		d, err := time.ParseDuration(v.GetString("voice.capture." + key))
		if err != nil || d <= 0 {
			return c, errors.New("voice capture durations must be positive durations")
		}
		*dest = d
	}
	if v.IsSet("voice.capture.threshold") {
		c.Threshold = v.GetFloat64("voice.capture.threshold")
	}
	if strings.TrimSpace(c.Executable) == "" {
		return c, errors.New("voice capture executable is required")
	}
	return c, validateCapture(voice.CaptureOptions{Target: c.Target, SilenceTimeout: c.SilenceTimeout, MaxDuration: c.MaxDuration, StartTimeout: c.StartTimeout, Threshold: c.Threshold})
}

func validateCapture(o voice.CaptureOptions) error {
	if o.SilenceTimeout <= 0 || o.SilenceTimeout > time.Minute || o.MaxDuration < 150*time.Millisecond || o.MaxDuration > 5*time.Minute || o.StartTimeout <= 0 || o.StartTimeout > 5*time.Minute {
		return errors.New("voice capture requires positive silence <= 1m, max duration in [150ms, 5m], and positive start timeout <= 5m")
	}
	if math.IsNaN(o.Threshold) || math.IsInf(o.Threshold, 0) || o.Threshold <= 0 || o.Threshold > 1 {
		return errors.New("voice capture threshold must be greater than zero and at most one")
	}
	if len(o.Target) > 1024 || strings.ContainsRune(o.Target, 0) {
		return errors.New("invalid voice capture target")
	}
	return nil
}

var captureLanguage = regexp.MustCompile(`^(auto|[a-z]{2,3})$`)

// Validate request semantics before allowing the microphone to start. Do not
// probe executables here: disabled/status/disable must work on broken installs.
func (v *voiceRuntime) listenOptions(m ipc.Message, continuous bool) (voice.CaptureOptions, error) {
	if !v.enabled {
		return voice.CaptureOptions{}, voice.ErrDisabled
	}
	if v.configErr != nil {
		return voice.CaptureOptions{}, v.configErr
	}
	if v.captureErr != nil {
		return voice.CaptureOptions{}, v.captureErr
	}
	if v.residency != "" && v.residency != "on_demand" {
		return voice.CaptureOptions{}, errors.New("voice.stt.residency supports only on_demand")
	}
	mode := m.Mode
	if mode == "" {
		mode = v.mode
	}
	if mode != "" && mode != "raw" && mode != "ai" {
		return voice.CaptureOptions{}, errors.New("voice mode must be raw or ai")
	}
	if m.Action != "" && m.Action != "auto" && m.Action != "create" && m.Action != "expand" {
		return voice.CaptureOptions{}, errors.New("invalid note action")
	}
	if (m.NoteID != "" || (m.Action != "" && m.Action != "auto")) && (!m.Save || mode != "ai") {
		return voice.CaptureOptions{}, errors.New("note actions require save and AI mode")
	}
	if (m.Action == "expand" && m.NoteID == "") || (m.Action == "create" && m.NoteID != "") {
		return voice.CaptureOptions{}, errors.New("invalid expansion target")
	}
	if len(m.Namespace) > 256 || strings.ContainsRune(m.Namespace, 0) {
		return voice.CaptureOptions{}, errors.New("invalid voice namespace")
	}
	if m.AudioPath != "" {
		return voice.CaptureOptions{}, errors.New("microphone capture does not accept an audio path")
	}
	language := m.Language
	if language == "" {
		language = v.sttConfig.Language
	}
	if language != "" && !captureLanguage.MatchString(language) {
		return voice.CaptureOptions{}, errors.New("invalid voice language")
	}
	if v.sttConfig.Device != "" && v.sttConfig.Device != "auto" && v.sttConfig.Device != "cpu" {
		return voice.CaptureOptions{}, errors.New("voice device must be auto or cpu")
	}
	if v.sttConfig.Threads < 0 || v.sttConfig.Threads > 256 {
		return voice.CaptureOptions{}, errors.New("voice threads must be between 1 and 256")
	}
	if m.Model == "" && v.sttConfig.Model == "" {
		return voice.CaptureOptions{}, errors.New("voice model is required")
	}
	c := v.captureConfig
	o := voice.CaptureOptions{Once: !continuous, Target: c.Target, SilenceTimeout: c.SilenceTimeout, MaxDuration: c.MaxDuration, StartTimeout: c.StartTimeout, Threshold: c.Threshold}
	if m.Capture != nil {
		x := m.Capture
		if x.Target != "" {
			o.Target = x.Target
		}
		if x.SilenceTimeout != 0 {
			o.SilenceTimeout = x.SilenceTimeout
		}
		if x.MaxDuration != 0 {
			o.MaxDuration = x.MaxDuration
		}
		if x.StartTimeout != 0 {
			o.StartTimeout = x.StartTimeout
		}
		if x.Threshold != 0 {
			o.Threshold = x.Threshold
		}
	}
	return o, validateCapture(o)
}

func (v *voiceRuntime) voiceStatus() ipc.Response {
	v.mu.Lock()
	defer v.mu.Unlock()
	s := v.status
	return ipc.Response{OK: true, VoiceStatus: &s}
}

func (v *voiceRuntime) listen(ctx context.Context, m ipc.Message, continuous bool) ipc.Response {
	if continuous {
		m.Save = true
	}
	o, err := v.listenOptions(m, continuous)
	if err != nil {
		return ipc.Response{Msg: err.Error()}
	}
	if err := ctx.Err(); err != nil {
		return ipc.Response{Msg: err.Error()}
	}
	if !m.Deadline.IsZero() && !time.Now().Before(m.Deadline) {
		return ipc.Response{Msg: context.DeadlineExceeded.Error()}
	}
	v.mu.Lock()
	if v.closed {
		v.mu.Unlock()
		return ipc.Response{Msg: "voice daemon is shutting down"}
	}
	if v.job != nil {
		v.mu.Unlock()
		return ipc.Response{Msg: "voice capture is busy"}
	}
	parent := v.root
	if parent == nil {
		parent = context.Background()
	}
	if err := parent.Err(); err != nil {
		v.mu.Unlock()
		return ipc.Response{Msg: err.Error()}
	}
	if !continuous {
		parent = ctx
	}
	jobCtx, cancel := context.WithCancel(parent)
	cleanup := func() {}
	if !continuous {
		root := v.root
		if root == nil {
			root = context.Background()
		}
		unlink := context.AfterFunc(root, cancel)
		cleanup = func() { unlink() }
		if !m.Deadline.IsZero() {
			var deadlineCancel context.CancelFunc
			jobCtx, deadlineCancel = context.WithDeadline(jobCtx, m.Deadline)
			cleanup = func() { unlink(); deadlineCancel() }
		}
	}
	// Continuous work is detached from the initiating connection and its deadline.
	m.Deadline = time.Time{}
	m.Tags = append([]string(nil), m.Tags...)
	j := &listenJob{cancel: cancel, stop: make(chan struct{}), done: make(chan struct{})}
	v.job = j
	v.status = ipc.VoiceStatus{Enabled: v.enabled, Active: true, Continuous: continuous, State: "listening", Namespace: m.Namespace, SilenceTimeout: o.SilenceTimeout, StartedAt: time.Now().UTC()}
	go func() { defer cleanup(); v.runListener(jobCtx, j, m, o) }()
	s := v.status
	v.mu.Unlock()
	if continuous {
		return ipc.Response{OK: true, VoiceStatus: &s}
	}
	<-j.done
	return j.result
}

// stopListener flushes/drains on stop and discards pending work on cancel. Both
// join the capture process AND the processing worker, even if the caller leaves.
func (v *voiceRuntime) stopListener(finalize bool) ipc.Response {
	v.mu.Lock()
	j := v.job
	if j != nil {
		if finalize {
			if !j.stopping {
				close(j.stop)
				j.stopping = true
			}
			v.status.State = "stopping"
		} else {
			j.stopping = true
			j.cancel()
			v.status.State = "canceling"
		}
	}
	v.mu.Unlock()
	if j != nil {
		<-j.done
	}
	r := v.voiceStatus()
	if finalize && j != nil && !j.result.OK {
		r.OK, r.Msg = false, j.result.Msg
	}
	return r
}

func (v *voiceRuntime) close() {
	v.mu.Lock()
	v.closed = true
	j := v.job
	if j != nil {
		j.cancel()
	}
	syncDone := v.syncDone
	if v.syncCancel != nil {
		v.syncCancel()
	}
	v.mu.Unlock()
	if j != nil {
		<-j.done
	}
	if syncDone != nil {
		<-syncDone
	}
}

func (v *voiceRuntime) runListener(ctx context.Context, j *listenJob, m ipc.Message, o voice.CaptureOptions) {
	defer j.cancel()
	queue := make(chan voice.Segment, captureQueueSize)
	captureDone := make(chan error, 1)
	var overloaded atomic.Bool
	var oversized atomic.Bool
	go func() {
		err := v.capturer.Capture(ctx, o, j.stop, func(s voice.Segment) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if len(s.WAV) > 32<<20 {
				oversized.Store(true)
				j.cancel()
				return errors.New("voice segment exceeds audio byte limit")
			}
			s.WAV = append([]byte(nil), s.WAV...)
			// The device stream is ending during a graceful stop, so waiting for
			// final-flush capacity cannot stall further device reads. Cancellation
			// (including concurrent disable) must still interrupt this drain.
			enqueueFinal := func() error {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case queue <- s:
					return nil
				}
			}
			if s.Reason == "stop" {
				return enqueueFinal()
			}
			select {
			case <-j.stop:
				return enqueueFinal()
			default:
			}
			select {
			case queue <- s:
				return nil
			default:
				select {
				case <-j.stop:
					return enqueueFinal()
				default:
				}
				overloaded.Store(true)
				j.cancel()
				return errCaptureOverload
			}
		})
		if err != nil {
			j.cancel()
		}
		close(queue)
		captureDone <- err
	}()
	result := ipc.Response{OK: true}
	processingFailed := false
	process := v.processSegment
	if process == nil {
		process = v.transcribeSegment
	}
	for segment := range queue {
		if ctx.Err() != nil || !result.OK {
			continue
		}
		v.mu.Lock()
		if !j.stopping {
			v.status.State = "processing"
		}
		v.mu.Unlock()
		result = process(ctx, m, segment)
		if !result.OK {
			processingFailed = ctx.Err() == nil
			j.cancel()
			continue
		}
		v.mu.Lock()
		v.status.Segments++
		if result.Entry != nil {
			v.status.LastNoteID = result.Entry.ID
		}
		if !j.stopping {
			v.status.State = "listening"
		}
		v.mu.Unlock()
		if result.Entry != nil {
			v.scheduleSync()
		}
	}
	captureErr := <-captureDone
	fault := ""
	switch {
	case overloaded.Load() || errors.Is(captureErr, errCaptureOverload):
		fault = errCaptureOverload.Error()
	case oversized.Load():
		fault = "voice segment exceeds audio byte limit"
	case captureErr != nil && !errors.Is(captureErr, context.Canceled) && !errors.Is(captureErr, context.DeadlineExceeded):
		fault = "voice capture failed; check capture executable and audio device, then enable again"
	case processingFailed:
		fault = "voice segment processing failed; check transcription and note configuration"
	case ctx.Err() != nil:
		fault = ctx.Err().Error()
	}
	if fault != "" {
		result = ipc.Response{OK: false, Msg: fault}
	}
	if result.OK && o.Once && result.Transcript == nil {
		result.Transcript = &ipc.Transcript{}
	}
	v.mu.Lock()
	v.status.Active, v.status.Continuous = false, false
	v.status.State = "idle"
	if fault != "" {
		v.status.State = "error"
		if ctx.Err() != nil && (fault == context.Canceled.Error() || fault == context.DeadlineExceeded.Error()) {
			v.status.State = "canceled"
		}
	}
	v.status.LastError = fault // Application-owned bounded messages, never stderr/audio/text.
	s := v.status
	result.VoiceStatus = &s
	j.result = result
	v.job = nil
	close(j.done)
	v.mu.Unlock()
}

func (v *voiceRuntime) transcribeSegment(ctx context.Context, m ipc.Message, segment voice.Segment) ipc.Response {
	if err := ctx.Err(); err != nil {
		return ipc.Response{Msg: err.Error()}
	}
	file, err := os.CreateTemp("", "ginkgo-capture-*.wav") // 0600; never a shared predictable pathname.
	if err != nil {
		return ipc.Response{Msg: "create private voice segment failed"}
	}
	path := file.Name()
	defer os.Remove(path)
	_, writeErr := file.Write(segment.WAV)
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		return ipc.Response{Msg: "write private voice segment failed"}
	}
	m.AudioPath = path
	return v.transcribe(ctx, m)
}
