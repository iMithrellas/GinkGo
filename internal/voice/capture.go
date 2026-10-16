package voice

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"sync/atomic"
	"syscall"
	"time"
)

// CaptureConfig selects a trusted installed pw-record. Construction does no I/O.
type CaptureConfig struct {
	Enabled        bool
	Executable     string
	Target         string
	SilenceTimeout time.Duration
	MaxDuration    time.Duration
	StartTimeout   time.Duration
	Threshold      float64
}

// CaptureOptions overrides nonzero configuration fields. Once waits for one
// utterance; otherwise one process remains open across all utterances.
type CaptureOptions struct {
	Once           bool
	Target         string
	SilenceTimeout time.Duration
	MaxDuration    time.Duration
	StartTimeout   time.Duration
	Threshold      float64
}

// Segment contains a complete mono 16 kHz signed 16-bit PCM WAV. Reason is
// "silence", "max_duration", or "stop".
type Segment struct {
	WAV    []byte
	Reason string
}

// Capturer serializes captures while keeping each continuous session's stream open.
type Capturer struct {
	config CaptureConfig
	slot   chan struct{}
}

// NewCapturer constructs a lazy capturer without probing devices or executables.
func NewCapturer(c CaptureConfig) *Capturer {
	return &Capturer{config: c, slot: make(chan struct{}, 1)}
}

func (c *Capturer) settings(o CaptureOptions) (CaptureConfig, error) {
	s := c.config
	if !s.Enabled {
		return s, ErrDisabled
	}
	if err := validateCaptureLimits(s.SilenceTimeout, s.MaxDuration, s.StartTimeout, s.Threshold); err != nil {
		return s, fmt.Errorf("capture configuration: %w", err)
	}
	if err := validateCaptureLimits(o.SilenceTimeout, o.MaxDuration, o.StartTimeout, o.Threshold); err != nil {
		return s, fmt.Errorf("capture options: %w", err)
	}
	if o.Target != "" {
		s.Target = o.Target
	}
	if o.SilenceTimeout != 0 {
		s.SilenceTimeout = o.SilenceTimeout
	}
	if o.MaxDuration != 0 {
		s.MaxDuration = o.MaxDuration
	}
	if o.StartTimeout != 0 {
		s.StartTimeout = o.StartTimeout
	}
	if o.Threshold != 0 {
		s.Threshold = o.Threshold
	}
	if s.Executable == "" {
		s.Executable = "pw-record"
	}
	if s.SilenceTimeout == 0 {
		s.SilenceTimeout = 1500 * time.Millisecond
	}
	if s.MaxDuration == 0 {
		s.MaxDuration = 2 * time.Minute
	}
	if s.StartTimeout == 0 {
		s.StartTimeout = 30 * time.Second
	}
	if s.Threshold == 0 {
		s.Threshold = .02
	}
	return s, nil
}

// Zero selects defaults; validate both sources before merging so an override
// cannot hide invalid configuration. Duration caps use whole PCM frames below.
func validateCaptureLimits(silence, maximum, start time.Duration, threshold float64) error {
	if silence < 0 || silence > time.Minute {
		return errors.New("silence timeout must be positive and <=1m (zero selects default)")
	}
	if maximum != 0 && (maximum < captureMinimumSpeech || maximum > 5*time.Minute) {
		return errors.New("max duration must be 150ms–5m (zero selects default)")
	}
	if start < 0 || start > 5*time.Minute {
		return errors.New("start timeout must be positive and <=5m (zero selects default)")
	}
	if threshold < 0 || threshold > 1 || math.IsNaN(threshold) || math.IsInf(threshold, 0) {
		return errors.New("RMS threshold must be finite and in (0,1] (zero selects default)")
	}
	return nil
}

const captureFrameBytes = 320 // 10 ms, 160 samples
const captureFrameDuration = 10 * time.Millisecond
const captureMinimumSpeech = 150 * time.Millisecond
const captureMinimumSpeechFrames = int(captureMinimumSpeech / captureFrameDuration)
const captureStallTimeout = 5 * time.Second

// Capture uses no shell or device probes. Stop finalizes qualifying speech;
// context cancellation discards partial audio. emit must return promptly (a
// daemon should enqueue into a bounded queue, not transcribe in this callback).
func (c *Capturer) Capture(ctx context.Context, opts CaptureOptions, stop <-chan struct{}, emit func(Segment) error) error {
	s, err := c.settings(opts)
	if err != nil {
		return err
	}
	if emit == nil {
		return errors.New("capture requires an emit callback")
	}
	select {
	case c.slot <- struct{}{}:
		defer func() { <-c.slot }()
	case <-ctx.Done():
		return ctx.Err()
	case <-stop:
		return ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-stop:
		return nil
	default:
	}
	childCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	args := []string{"--raw", "--format", "s16", "--rate", "16000", "--channels", "1"}
	if s.Target != "" {
		args = append(args, "--target", s.Target)
	}
	args = append(args, "-")
	cmd := exec.CommandContext(childCtx, s.Executable, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	kill := func() {
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
	}
	cmd.Cancel = func() error { kill(); return nil }
	cmd.WaitDelay = time.Second
	diagnostics := &captureDiagnostics{cancel: cancel}
	cmd.Stderr = diagnostics
	reader, writer, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("capture pipe: %w", err)
	}
	defer reader.Close()
	defer writer.Close()
	cmd.Stdout = writer
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start pw-record (install PipeWire tools and verify capture executable): %w", err)
	}
	defer kill()
	_ = writer.Close()
	wait := make(chan error, 1)
	go func() { e := cmd.Wait(); kill(); wait <- e }()
	frames := make(chan captureFrame, 8)
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		for {
			var f captureFrame
			_, f.err = io.ReadFull(reader, f.data[:])
			select {
			case frames <- f:
			case <-childCtx.Done():
				return
			}
			if f.err != nil {
				return
			}
		}
	}()
	err = captureFrames(childCtx, s, opts.Once, stop, frames, emit)
	cancel()
	kill()
	_ = reader.Close()
	<-readDone
	waitErr := <-wait
	if diagnostics.exceeded.Load() {
		return errors.New("pw-record stderr exceeds 64 KiB limit")
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil && waitErr != nil {
		return fmt.Errorf("%w (pw-record: %v; verify PipeWire service, source target and permissions)", err, waitErr)
	}
	return err
}

type captureDiagnostics struct {
	bytes    int
	exceeded atomic.Bool
	cancel   context.CancelFunc
}

func (d *captureDiagnostics) Write(p []byte) (int, error) {
	if len(p) > (64<<10)-d.bytes {
		d.exceeded.Store(true)
		d.cancel()
	} else {
		d.bytes += len(p)
	}
	return len(p), nil
}

type captureFrame struct {
	data [captureFrameBytes]byte
	err  error
}

func captureFrames(ctx context.Context, s CaptureConfig, once bool, stop <-chan struct{}, frames <-chan captureFrame, emit func(Segment) error) error {
	v := newCaptureVAD(s)
	stall := time.NewTimer(captureStallTimeout)
	defer stall.Stop()
	var start <-chan time.Time
	var utterance <-chan time.Time
	utteranceTimer := time.NewTimer(time.Hour)
	utteranceTimer.Stop()
	defer utteranceTimer.Stop()
	resetUtterance := func() {
		if !utteranceTimer.Stop() {
			select {
			case <-utteranceTimer.C:
			default:
			}
		}
		utterance = nil
		v = newCaptureVAD(s)
	}
	if once {
		timer := time.NewTimer(s.StartTimeout)
		defer timer.Stop()
		start = timer.C
	}
	deliver := func(reason string) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if v.speechFrames < captureMinimumSpeechFrames {
			return nil
		}
		return emit(Segment{WAV: captureWAV(v.audio), Reason: reason})
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-stop:
			return deliver("stop")
		case <-start:
			if v.speechFrames < captureMinimumSpeechFrames {
				return nil
			}
			start = nil
		case <-stall.C:
			return errors.New("pw-record produced no complete PCM frame for 5s; verify PipeWire source and permissions")
		case <-utterance:
			qualifies := v.speechFrames >= captureMinimumSpeechFrames
			if err := deliver("max_duration"); err != nil {
				return err
			}
			resetUtterance()
			if once && qualifies {
				return nil
			}
		case f, ok := <-frames:
			if !ok {
				return errors.New("pw-record PCM stream ended unexpectedly")
			}
			if f.err != nil {
				return fmt.Errorf("pw-record PCM stream ended (device lost or recorder exited): %w", f.err)
			}
			if !stall.Stop() {
				select {
				case <-stall.C:
				default:
				}
			}
			stall.Reset(captureStallTimeout)
			reason := v.add(f.data[:])
			if len(v.audio) > 0 && utterance == nil {
				utteranceTimer.Reset(s.MaxDuration)
				utterance = utteranceTimer.C
			}
			if v.speechFrames >= captureMinimumSpeechFrames {
				start = nil
			}
			if reason != "" {
				qualifies := v.speechFrames >= captureMinimumSpeechFrames
				if err := deliver(reason); err != nil {
					return err
				}
				resetUtterance()
				if once && qualifies {
					return nil
				}
			}
		}
	}
}

type captureVAD struct {
	config       CaptureConfig
	pre, audio   []byte
	speechFrames int
	silence      time.Duration
	maxBytes     int
}

func newCaptureVAD(s CaptureConfig) *captureVAD {
	// Round down to whole frames: no truncated frame can count as 10ms of
	// speech, and output duration never exceeds the caller's declared bound.
	return &captureVAD{config: s, pre: make([]byte, 0, 6400), maxBytes: int(s.MaxDuration/captureFrameDuration) * captureFrameBytes}
}
func (v *captureVAD) add(frame []byte) string {
	var energy float64
	for i := 0; i < len(frame); i += 2 {
		x := float64(int16(binary.LittleEndian.Uint16(frame[i:]))) / 32768
		energy += x * x
	}
	speech := math.Sqrt(energy/float64(len(frame)/2)) >= v.config.Threshold
	if len(v.audio) == 0 && !speech {
		v.pre = append(v.pre, frame...)
		if len(v.pre) > 6400 {
			copy(v.pre, v.pre[len(v.pre)-6400:])
			v.pre = v.pre[:6400]
		}
		return ""
	}
	if len(v.audio) == 0 {
		// Reserve enough room for the minimum speech, not merely its first
		// frame. A 150ms cap therefore drops all pre-roll instead of speech.
		pre := v.pre
		preLimit := max(0, v.maxBytes-captureMinimumSpeechFrames*captureFrameBytes)
		if len(pre) > preLimit {
			pre = pre[len(pre)-preLimit:]
		}
		v.audio = append(v.audio, pre...)
	}
	remaining := v.maxBytes - len(v.audio)
	if remaining < len(frame) {
		frame = frame[:remaining]
	}
	v.audio = append(v.audio, frame...)
	if speech {
		v.speechFrames++
		v.silence = 0
	} else {
		v.silence += captureFrameDuration
	}
	if len(v.audio) >= v.maxBytes {
		return "max_duration"
	}
	if v.silence >= v.config.SilenceTimeout {
		return "silence"
	}
	return ""
}

func captureWAV(pcm []byte) []byte {
	b := make([]byte, 44+len(pcm))
	copy(b, "RIFF")
	binary.LittleEndian.PutUint32(b[4:], uint32(len(b)-8))
	copy(b[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(b[16:], 16)
	binary.LittleEndian.PutUint16(b[20:], 1)
	binary.LittleEndian.PutUint16(b[22:], 1)
	binary.LittleEndian.PutUint32(b[24:], 16000)
	binary.LittleEndian.PutUint32(b[28:], 32000)
	binary.LittleEndian.PutUint16(b[32:], 2)
	binary.LittleEndian.PutUint16(b[34:], 16)
	copy(b[36:], "data")
	binary.LittleEndian.PutUint32(b[40:], uint32(len(pcm)))
	copy(b[44:], pcm)
	return b
}
