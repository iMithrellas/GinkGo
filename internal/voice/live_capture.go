package voice

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"time"
)

// PCMChunk is owned mono PCM16 little-endian, 16 kHz audio. Offsets are absolute
// samples since this capture started, not bytes or wall-clock timestamps.
type PCMChunk struct {
	StartSample int64
	PCM         []byte
}

// LiveCaptureOptions selects continuous capture. Zero durations select 500ms
// updates and 5s rolling windows. Once is not supported; stop ends the stream.
type LiveCaptureOptions struct {
	CaptureOptions
	UpdateInterval time.Duration
	WindowDuration time.Duration
	// ObservePCM optionally feeds a bounded detector/pre-roll queue, including
	// idle audio. Like emit, it must return promptly and must not run inference.
	ObservePCM func(PCMChunk) error
}

// LiveCaptureEvent is an immutable snapshot. Kind is activity, partial, or final.
// Partials REPLACE the indicated window, never append text. Final WAVs cover the
// entire utterance, not just its last rolling window. IDs are capture-local;
// callers must namespace them with a stream identity across restarts.
type LiveCaptureEvent struct {
	Kind                 string
	UtteranceID          uint64
	Revision             uint64
	StartSample          int64
	UtteranceStartSample int64 // absolute onset including pre-roll; rolling windows retain this origin
	EndSample            int64 // exclusive
	WAV                  []byte
	Level                float64
	Speech               bool   // current frame's RMS decision, not a trained speech model
	State                string // idle, speech, or silence
	Reason               string // final only: silence, max_duration, stop, device_lost, stall
}

func liveSettings(o LiveCaptureOptions) (LiveCaptureOptions, error) {
	if o.Once {
		return o, errors.New("live capture does not support Once")
	}
	if o.UpdateInterval == 0 {
		o.UpdateInterval = 500 * time.Millisecond
	}
	if o.WindowDuration == 0 {
		o.WindowDuration = 5 * time.Second
	}
	if o.UpdateInterval < captureFrameDuration || o.UpdateInterval > 5*time.Second {
		return o, errors.New("live update interval must be 10ms–5s")
	}
	if o.WindowDuration < captureMinimumSpeech || o.WindowDuration > 30*time.Second {
		return o, errors.New("live window duration must be 150ms–30s")
	}
	return o, nil
}

// CaptureLive streams captions independently of note activation using one
// pw-record process. Callbacks are synchronous: enqueue/coalesce partials in a
// bounded queue, reserve space for finals, and return errors on overflow. A
// callback error aborts capture; no final is promised after callback failure.
// Stop flushes consumed qualifying audio; cancellation discards it. Device loss
// and stall flush qualifying audio before returning an actionable error.
func (c *Capturer) CaptureLive(ctx context.Context, opts LiveCaptureOptions, stop <-chan struct{}, emit func(LiveCaptureEvent) error) error {
	s, err := c.settings(opts.CaptureOptions)
	if err != nil {
		return err
	}
	opts, err = liveSettings(opts)
	if err != nil {
		return err
	}
	if emit == nil {
		return errors.New("live capture requires an emit callback")
	}
	return c.runCapture(ctx, s, stop, func(ctx context.Context, frames <-chan captureFrame) error {
		return liveCaptureFrames(ctx, s, opts, stop, frames, emit)
	})
}

type liveCapture struct {
	config                  CaptureConfig
	opts                    LiveCaptureOptions
	ctx                     context.Context
	emit                    func(LiveCaptureEvent) error
	v                       *captureVAD
	next, start, lastUpdate int64
	id, revision            uint64
	level                   float64
	speech, dirty           bool
}

func newLiveCapture(ctx context.Context, s CaptureConfig, o LiveCaptureOptions, emit func(LiveCaptureEvent) error) *liveCapture {
	return &liveCapture{config: s, opts: o, ctx: ctx, emit: emit, v: newCaptureVAD(s)}
}

func (l *liveCapture) event(kind, reason string, pcm []byte, start int64) error {
	if err := l.ctx.Err(); err != nil {
		return err
	}
	state := "idle"
	if len(l.v.audio) > 0 {
		state = "silence"
		if l.speech {
			state = "speech"
		}
	}
	e := LiveCaptureEvent{Kind: kind, UtteranceID: l.id, Revision: l.revision,
		StartSample: start, EndSample: l.next, Level: l.level, Speech: l.speech, State: state, Reason: reason}
	e.UtteranceStartSample = l.start
	if pcm != nil {
		e.WAV = captureWAV(pcm)
	}
	if kind == "activity" && len(l.v.audio) == 0 {
		e.UtteranceID = 0
	}
	return l.emit(e)
}

func (l *liveCapture) finish(reason string) error {
	if err := l.ctx.Err(); err != nil {
		return err
	}
	if l.v.speechFrames >= captureMinimumSpeechFrames {
		l.revision++
		if err := l.event("final", reason, l.v.audio, l.start); err != nil {
			return err
		}
	}
	l.v = newCaptureVAD(l.config)
	l.revision = 0
	l.dirty = false
	return nil
}

func (l *liveCapture) add(f captureFrame) error {
	var energy float64
	for i := 0; i < len(f.data); i += 2 {
		x := float64(int16(binary.LittleEndian.Uint16(f.data[i:]))) / 32768
		energy += x * x
	}
	l.level = math.Sqrt(energy / float64(captureFrameBytes/2))
	l.speech = l.level >= l.config.Threshold
	wasIdle := len(l.v.audio) == 0
	start := l.next
	l.next += captureFrameBytes / 2
	reason := l.v.add(f.data[:])
	if wasIdle && len(l.v.audio) > 0 {
		l.id++
		l.start = l.next - int64(len(l.v.audio)/2)
		l.lastUpdate = start
	}
	l.dirty = l.dirty || l.speech
	if l.opts.ObservePCM != nil {
		if err := l.opts.ObservePCM(PCMChunk{StartSample: start, PCM: append([]byte(nil), f.data[:]...)}); err != nil {
			return err
		}
	}
	if reason != "" {
		return l.finish(reason)
	}
	interval := int64(l.opts.UpdateInterval/captureFrameDuration) * (captureFrameBytes / 2)
	if l.next-l.lastUpdate < interval {
		return nil
	}
	l.lastUpdate = l.next
	if l.v.speechFrames >= captureMinimumSpeechFrames && l.dirty {
		limit := int(l.opts.WindowDuration/captureFrameDuration) * captureFrameBytes
		pcm := l.v.audio[max(0, len(l.v.audio)-limit):]
		l.revision++
		l.dirty = false
		return l.event("partial", "", pcm, l.next-int64(len(pcm)/2))
	}
	return l.event("activity", "", nil, l.next)
}

func liveCaptureFrames(ctx context.Context, s CaptureConfig, o LiveCaptureOptions, stop <-chan struct{}, frames <-chan captureFrame, emit func(LiveCaptureEvent) error) error {
	l := newLiveCapture(ctx, s, o, emit)
	stall := time.NewTimer(captureStallTimeout)
	defer stall.Stop()
	maximum := time.NewTimer(time.Hour)
	maximum.Stop()
	defer maximum.Stop()
	var maxC <-chan time.Time
	resetMaximum := func() {
		if !maximum.Stop() {
			select {
			case <-maximum.C:
			default:
			}
		}
		maxC = nil
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		// Prioritize stop over queued frames. The boundary is consumed PCM, not
		// arbitrary unread kernel/process buffers or future queued samples.
		select {
		case <-stop:
			return l.finish("stop")
		default:
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-stop:
			return l.finish("stop")
		case <-stall.C:
			if err := l.finish("stall"); err != nil {
				return err
			}
			return errors.New("pw-record produced no complete PCM frame for 5s; verify PipeWire source and permissions")
		case <-maxC:
			if err := l.finish("max_duration"); err != nil {
				return err
			}
			resetMaximum()
		case f, ok := <-frames:
			if !ok || f.err != nil {
				if err := l.finish("device_lost"); err != nil {
					return err
				}
				if !ok {
					return errors.New("pw-record PCM stream ended unexpectedly")
				}
				return fmt.Errorf("pw-record PCM stream ended (device lost or recorder exited): %w", f.err)
			}
			if !stall.Stop() {
				select {
				case <-stall.C:
				default:
				}
			}
			stall.Reset(captureStallTimeout)
			if err := l.add(f); err != nil {
				return err
			}
			if len(l.v.audio) == 0 {
				resetMaximum()
			} else if maxC == nil {
				maximum.Reset(s.MaxDuration)
				maxC = maximum.C
			}
		}
	}
}
