package voice

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func liveTestOptions(t *testing.T, o LiveCaptureOptions) LiveCaptureOptions {
	t.Helper()
	o, err := liveSettings(o)
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func TestLivePreviewRollingRevisionAndExactFinal(t *testing.T) {
	s := captureTestConfig()
	s.SilenceTimeout = 20 * time.Millisecond
	o := liveTestOptions(t, LiveCaptureOptions{UpdateInterval: 200 * time.Millisecond, WindowDuration: 300 * time.Millisecond})
	var events []LiveCaptureEvent
	l := newLiveCapture(context.Background(), s, o, func(e LiveCaptureEvent) error { events = append(events, e); return nil })
	var expected []byte
	for i := 0; i < 100; i++ {
		f := pcmFrames(1, int16(3000+i))[0]
		expected = append(expected, f.data[:]...)
		if err := l.add(f); err != nil {
			t.Fatal(err)
		}
		if i == 19 && (len(events) != 1 || events[0].Kind != "partial") {
			t.Fatal("no preview before silence")
		}
	}
	for _, f := range pcmFrames(2, 0) {
		expected = append(expected, f.data[:]...)
		if err := l.add(f); err != nil {
			t.Fatal(err)
		}
	}
	last := events[len(events)-1]
	if last.Kind != "final" || last.Reason != "silence" || last.StartSample != 0 || last.EndSample != 16320 || !bytes.Equal(last.WAV[44:], expected) {
		t.Fatalf("final: %+v", last)
	}
	for i, e := range events {
		if e.UtteranceStartSample != 0 {
			t.Fatal("rolling revision changed utterance origin")
		}
		if e.UtteranceID != 1 || e.Revision != uint64(i+1) {
			t.Fatalf("identity/revision: %+v", e)
		}
		if e.Kind == "partial" {
			if len(e.WAV) > 44+9600 || !bytes.Equal(e.WAV[44:], expected[e.StartSample*2:e.EndSample*2]) {
				t.Fatal("rolling window is not an exact revision")
			}
		}
	}
	if events[len(events)-2].StartSample == 0 {
		t.Fatal("rolling tail lacks window offset")
	}
	// A subsequent utterance is a different ID with the same absolute clock.
	for _, f := range pcmFrames(20, 4000) {
		if err := l.add(f); err != nil {
			t.Fatal(err)
		}
	}
	if events[len(events)-1].UtteranceID != 2 || events[len(events)-1].StartSample != 16320 || events[len(events)-1].UtteranceStartSample != 16320 {
		t.Fatal("next utterance identity")
	}
}

func TestLiveTinyCadenceDiscardsSubminimumActivityWithoutFinal(t *testing.T) {
	// RMS qualification counts complete 10ms frames. Exercise discarded pulses
	// just below the 15-frame threshold and exactly at its 150ms boundary.
	for _, speechFrames := range []int{1, 14, 15} {
		t.Run((time.Duration(speechFrames) * 10 * time.Millisecond).String(), func(t *testing.T) {
			s := captureTestConfig()
			s.SilenceTimeout = 20 * time.Millisecond
			var events []LiveCaptureEvent
			l := newLiveCapture(context.Background(), s, liveTestOptions(t, LiveCaptureOptions{UpdateInterval: 10 * time.Millisecond}), func(e LiveCaptureEvent) error {
				e.WAV = nil
				events = append(events, e)
				return nil
			})
			for _, group := range [][]captureFrame{pcmFrames(speechFrames, 4000), pcmFrames(3, 0), pcmFrames(15, 5000), pcmFrames(3, 0)} {
				for _, frame := range group {
					if err := l.add(frame); err != nil {
						t.Fatal(err)
					}
				}
			}
			var activity, partials, finals int
			for _, e := range events {
				if e.UtteranceID == 1 {
					switch e.Kind {
					case "activity":
						activity++
					case "partial":
						partials++
					case "final":
						finals++
					}
				}
			}
			if activity == 0 {
				t.Fatal("tiny cadence did not expose provisional activity")
			}
			if speechFrames < 15 && (partials != 0 || finals != 0) {
				t.Fatal("discarded subminimum utterance emitted qualifying audio")
			}
			if speechFrames == 15 && (partials != 1 || finals != 1) {
				t.Fatal("minimum speech did not qualify exactly once")
			}
			last := events[len(events)-2] // final is followed by one idle activity frame
			if last.Kind != "final" || last.UtteranceID != 2 {
				t.Fatalf("next qualifying utterance lost after short pulse: %+v", last)
			}
		})
	}
}

func TestLiveMaximumHasNoDuplicateOrMissingSamples(t *testing.T) {
	s := captureTestConfig()
	s.MaxDuration = 200 * time.Millisecond
	var got []byte
	var ends []int64
	l := newLiveCapture(context.Background(), s, liveTestOptions(t, LiveCaptureOptions{}), func(e LiveCaptureEvent) error {
		if e.Kind == "final" {
			got = append(got, e.WAV[44:]...)
			ends = append(ends, e.EndSample)
		}
		return nil
	})
	var want []byte
	for i := 0; i < 55; i++ {
		f := pcmFrames(1, int16(3000+i))[0]
		want = append(want, f.data[:]...)
		if err := l.add(f); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.finish("stop"); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) || len(ends) != 3 || ends[0] != 3200 || ends[1] != 6400 || ends[2] != 8800 {
		t.Fatalf("missing/duplicated audio: ends=%v len=%d", ends, len(got))
	}
}

func TestLiveIdleActivityAndMemoryBounds(t *testing.T) {
	s := captureTestConfig()
	o := liveTestOptions(t, LiveCaptureOptions{})
	count := 0
	l := newLiveCapture(context.Background(), s, o, func(e LiveCaptureEvent) error {
		count++
		if e.Kind != "activity" || e.UtteranceID != 0 || e.WAV != nil || e.Speech || e.State != "idle" {
			t.Fatal("idle sent inference audio")
		}
		return nil
	})
	for i := 0; i < 10000; i++ {
		if err := l.add(captureFrame{}); err != nil {
			t.Fatal(err)
		}
	}
	if len(l.v.pre) != 6400 || len(l.v.audio) != 0 || cap(l.v.pre) > 12800 || count != 200 {
		t.Fatalf("bounds/count: %d %d", cap(l.v.pre), count)
	}
}

func TestLiveStopCancelAndDeviceLoss(t *testing.T) {
	for _, mode := range []string{"stop", "cancel", "loss"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			stop := make(chan struct{})
			count := 0
			o := liveTestOptions(t, LiveCaptureOptions{ObservePCM: func(p PCMChunk) error {
				if p.StartSample == 14*160 {
					if mode == "stop" {
						close(stop)
					}
					if mode == "cancel" {
						cancel()
					}
				}
				return nil
			}})
			q := frameQueue(pcmFrames(15, 4000))
			close(q)
			err := liveCaptureFrames(ctx, captureTestConfig(), o, stop, q, func(e LiveCaptureEvent) error {
				if e.Kind == "final" {
					count++
					want := mode
					if mode == "loss" {
						want = "device_lost"
					}
					if e.Reason != want || len(e.WAV) != 4844 {
						t.Fatalf("bad flush: %+v", e)
					}
				}
				return nil
			})
			if mode == "cancel" {
				if !errors.Is(err, context.Canceled) || count != 0 {
					t.Fatalf("%v count=%d", err, count)
				}
			} else if count != 1 || (mode == "stop" && err != nil) || (mode == "loss" && err == nil) {
				t.Fatalf("%v count=%d", err, count)
			}
		})
	}
}

func TestLiveCallbackErrorsAndTruncatedDeviceLoss(t *testing.T) {
	want := errors.New("bounded queue full")
	for _, kind := range []string{"partial", "final", "pcm"} {
		o := liveTestOptions(t, LiveCaptureOptions{UpdateInterval: 150 * time.Millisecond})
		if kind == "pcm" {
			o.ObservePCM = func(PCMChunk) error { return want }
		}
		q := frameQueue(pcmFrames(15, 4000), []captureFrame{{err: io.ErrUnexpectedEOF}})
		err := liveCaptureFrames(context.Background(), captureTestConfig(), o, nil, q, func(e LiveCaptureEvent) error {
			if e.Kind == kind {
				return want
			}
			return nil
		})
		if !errors.Is(err, want) {
			t.Fatalf("%s: %v", kind, err)
		}
	}
	q := frameQueue(pcmFrames(15, 4000), []captureFrame{{err: io.ErrUnexpectedEOF}})
	finals := 0
	err := liveCaptureFrames(context.Background(), captureTestConfig(), liveTestOptions(t, LiveCaptureOptions{}), nil, q, func(e LiveCaptureEvent) error {
		if e.Kind == "final" {
			finals++
		}
		return nil
	})
	if !errors.Is(err, io.ErrUnexpectedEOF) || finals != 1 {
		t.Fatalf("%v finals=%d", err, finals)
	}
}

func TestLiveProcessOneRecorderAndCleanup(t *testing.T) {
	pcm := capturePCMFile(t, pcmFrames(50, 4000))
	calls := filepath.Join(t.TempDir(), "calls")
	exe := captureScript(t, "echo call >> '"+calls+"'\ncat '"+pcm+"'\nexec sleep 30\n")
	stop := make(chan struct{})
	finals := 0
	err := NewCapturer(CaptureConfig{Enabled: true, Executable: exe, MaxDuration: 250 * time.Millisecond}).CaptureLive(context.Background(), LiveCaptureOptions{UpdateInterval: 150 * time.Millisecond}, stop, func(e LiveCaptureEvent) error {
		if e.Kind == "final" {
			finals++
			if finals == 2 {
				close(stop)
			}
		}
		return nil
	})
	data, _ := os.ReadFile(calls)
	if err != nil || finals != 2 || string(data) != "call\n" {
		t.Fatalf("%v finals=%d calls=%q", err, finals, data)
	}
	for _, tc := range []struct{ body, want string }{{"exit 42\n", "stream ended"}, {"head -c 70000 /dev/zero >&2\nexec sleep 30\n", "stderr exceeds"}} {
		err = NewCapturer(CaptureConfig{Enabled: true, Executable: captureScript(t, tc.body)}).CaptureLive(context.Background(), LiveCaptureOptions{}, nil, func(LiveCaptureEvent) error { return nil })
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%v", err)
		}
	}
}

func TestLiveValidationAndDisabledIsolation(t *testing.T) {
	for _, o := range []LiveCaptureOptions{{CaptureOptions: CaptureOptions{Once: true}}, {UpdateInterval: -1}, {UpdateInterval: time.Second * 6}, {WindowDuration: -1}, {WindowDuration: time.Minute}} {
		if _, err := liveSettings(o); err == nil {
			t.Fatal("accepted invalid live options")
		}
	}
	err := NewCapturer(CaptureConfig{Executable: "/does/not/exist"}).CaptureLive(context.Background(), LiveCaptureOptions{UpdateInterval: -1}, nil, nil)
	if !errors.Is(err, ErrDisabled) {
		t.Fatal(err)
	}
}

func TestLiveWallMaximumAndSharedAdmission(t *testing.T) {
	s := captureTestConfig()
	s.MaxDuration = 200 * time.Millisecond
	stop := make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := liveCaptureFrames(ctx, s, liveTestOptions(t, LiveCaptureOptions{}), stop, frameQueue(pcmFrames(15, 4000)), func(e LiveCaptureEvent) error {
		if e.Kind == "final" {
			if e.Reason != "max_duration" || len(e.WAV) != 4844 {
				t.Fatal("wall maximum did not retain complete consumed PCM")
			}
			close(stop)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// A legacy capture holding the shared slot prevents any live process spawn.
	c := NewCapturer(CaptureConfig{Enabled: true, Executable: "/does/not/exist"})
	c.slot <- struct{}{}
	queuedCtx, queuedCancel := context.WithCancel(context.Background())
	queuedCancel()
	err = c.CaptureLive(queuedCtx, LiveCaptureOptions{}, nil, func(LiveCaptureEvent) error { t.Fatal("queued callback"); return nil })
	if !errors.Is(err, context.Canceled) || len(c.slot) != 1 {
		t.Fatal("admission bypassed", err)
	}
	<-c.slot
}
