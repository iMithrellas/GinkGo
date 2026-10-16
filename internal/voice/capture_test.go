package voice

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func captureTestConfig() CaptureConfig {
	s, err := NewCapturer(CaptureConfig{Enabled: true}).settings(CaptureOptions{})
	if err != nil {
		panic(err)
	}
	return s
}

func pcmFrames(n int, amplitude int16) []captureFrame {
	result := make([]captureFrame, n)
	for j := range result {
		for i := 0; i < captureFrameBytes; i += 2 {
			binary.LittleEndian.PutUint16(result[j].data[i:], uint16(amplitude))
		}
	}
	return result
}

func frameQueue(groups ...[]captureFrame) chan captureFrame {
	n := 0
	for _, g := range groups {
		n += len(g)
	}
	ch := make(chan captureFrame, n)
	for _, g := range groups {
		for _, f := range g {
			ch <- f
		}
	}
	return ch
}

func TestCaptureDefaultsAndValidation(t *testing.T) {
	s := captureTestConfig()
	if s.SilenceTimeout != 1500*time.Millisecond || s.MaxDuration != 2*time.Minute || s.StartTimeout != 30*time.Second || s.Threshold != .02 || s.Executable != "pw-record" {
		t.Fatalf("defaults: %+v", s)
	}
	for _, o := range []CaptureOptions{
		{MaxDuration: -1}, {MaxDuration: 100 * time.Millisecond}, {MaxDuration: 150*time.Millisecond - 1},
		{SilenceTimeout: -1}, {SilenceTimeout: time.Minute + 1},
		{StartTimeout: -1}, {StartTimeout: 5*time.Minute + 1},
		{Threshold: -1}, {Threshold: math.NaN()}, {Threshold: math.Inf(1)}, {Threshold: math.Inf(-1)},
		{MaxDuration: 5*time.Minute + 1}, {Threshold: 1.1},
	} {
		if _, err := NewCapturer(s).settings(o); err == nil {
			t.Fatalf("accepted %+v", o)
		}
		config := CaptureConfig{Enabled: true, MaxDuration: o.MaxDuration, SilenceTimeout: o.SilenceTimeout, StartTimeout: o.StartTimeout, Threshold: o.Threshold}
		for _, overrides := range []CaptureOptions{{}, {MaxDuration: time.Second, SilenceTimeout: time.Second, StartTimeout: time.Second, Threshold: .1}} {
			if _, err := NewCapturer(config).settings(overrides); err == nil {
				t.Fatalf("accepted config %+v with overrides %+v", config, overrides)
			}
		}
	}
	if _, err := NewCapturer(s).settings(CaptureOptions{MaxDuration: 150 * time.Millisecond, SilenceTimeout: time.Minute, StartTimeout: 5 * time.Minute, Threshold: 1}); err != nil {
		t.Fatalf("rejected inclusive limits: %v", err)
	}
	c := NewCapturer(CaptureConfig{Executable: "/does/not/exist", Threshold: -1})
	if err := c.Capture(context.Background(), CaptureOptions{}, nil, nil); !errors.Is(err, ErrDisabled) {
		t.Fatal(err)
	}
	if _, err := NewCapturer(s).settings(CaptureOptions{Target: "source", Threshold: .1, MaxDuration: time.Second}); err != nil {
		t.Fatal(err)
	}
}

func TestCaptureVADPreRollMinimumAndSilence(t *testing.T) {
	s := captureTestConfig()
	s.SilenceTimeout = 30 * time.Millisecond
	frames := frameQueue(pcmFrames(30, 0), pcmFrames(14, 4000), pcmFrames(3, 0), pcmFrames(30, 0), pcmFrames(15, 4000), pcmFrames(3, 0))
	var got []Segment
	err := captureFrames(context.Background(), s, true, nil, frames, func(seg Segment) error { got = append(got, seg); return nil })
	if err != nil || len(got) != 1 {
		t.Fatalf("%v, %d segments", err, len(got))
	}
	seg := got[0]
	if seg.Reason != "silence" || len(seg.WAV) != 44+(20+15+3)*captureFrameBytes {
		t.Fatalf("segment: %s %d", seg.Reason, len(seg.WAV))
	}
	if !bytes.Equal(seg.WAV[44:44+6400], make([]byte, 6400)) {
		t.Fatal("pre-roll is not silence")
	}
	if err := validateWAV(seg.WAV, 5*time.Minute); err != nil {
		t.Fatal(err)
	}
}

func TestCaptureContinuousAndMaximum(t *testing.T) {
	s := captureTestConfig()
	s.MaxDuration = 200 * time.Millisecond
	s.SilenceTimeout = 20 * time.Millisecond
	stop := make(chan struct{})
	count := 0
	frames := frameQueue(pcmFrames(40, 4000))
	err := captureFrames(context.Background(), s, false, stop, frames, func(seg Segment) error {
		count++
		if seg.Reason != "max_duration" || len(seg.WAV) != 6444 {
			t.Fatalf("%s %d", seg.Reason, len(seg.WAV))
		}
		if count == 2 {
			close(stop)
		}
		return nil
	})
	if err != nil || count != 2 {
		t.Fatalf("%v count=%d", err, count)
	}
	// Silence endpoint also resets VAD without closing a continuous stream.
	frames = frameQueue(pcmFrames(15, 4000), pcmFrames(2, 0), pcmFrames(15, 4000), pcmFrames(2, 0))
	stop = make(chan struct{})
	count = 0
	s.MaxDuration = time.Second
	err = captureFrames(context.Background(), s, false, stop, frames, func(seg Segment) error {
		count++
		if count == 2 {
			close(stop)
		}
		return nil
	})
	if err != nil || count != 2 {
		t.Fatalf("%v count=%d", err, count)
	}
}

func TestCaptureHardDurationBound(t *testing.T) {
	s := captureTestConfig()
	s.MaxDuration = 5 * time.Minute
	v := newCaptureVAD(s)
	f := pcmFrames(1, -4000)[0]
	for i := 0; i < 30000; i++ {
		reason := v.add(f.data[:])
		if (i == 29999) != (reason == "max_duration") {
			t.Fatalf("frame %d reason %q", i, reason)
		}
	}
	wav := captureWAV(v.audio)
	if len(wav) > 32<<20 || len(wav) != 44+5*60*32000 {
		t.Fatalf("WAV size %d", len(wav))
	}
	if err := validateWAV(wav, 5*time.Minute); err != nil {
		t.Fatal(err)
	}
}

func TestCaptureSmallDurationReservesMinimumSpeech(t *testing.T) {
	for _, duration := range []time.Duration{
		150 * time.Millisecond, 150*time.Millisecond + 1, 155 * time.Millisecond,
		159*time.Millisecond + 999999, 160 * time.Millisecond, 161062500 * time.Nanosecond,
		199*time.Millisecond + 999999, 350 * time.Millisecond, 355 * time.Millisecond,
		400 * time.Millisecond,
	} {
		t.Run(duration.String(), func(t *testing.T) {
			config, err := NewCapturer(captureTestConfig()).settings(CaptureOptions{MaxDuration: duration})
			if err != nil {
				t.Fatal(err)
			}
			// All inputs are queued before running VAD, independent of clocks or
			// process scheduling. Prior silence must not crowd out qualifying speech.
			frames := frameQueue(pcmFrames(20, 0), pcmFrames(100, 4000))
			var segments []Segment
			err = captureFrames(context.Background(), config, true, nil, frames, func(segment Segment) error {
				segments = append(segments, segment)
				return nil
			})
			if err != nil || len(segments) != 1 {
				t.Fatalf("segments=%d err=%v", len(segments), err)
			}
			segment := segments[0]
			if segment.Reason != "max_duration" {
				t.Fatal(segment.Reason)
			}
			if err := validateWAV(segment.WAV, duration); err != nil {
				t.Fatal(err)
			}
			maxFrames := int(duration / (10 * time.Millisecond))
			if len(segment.WAV) != 44+maxFrames*captureFrameBytes || len(segment.WAV) > 32<<20 {
				t.Fatalf("WAV size %d for %s", len(segment.WAV), duration)
			}
			preFrames := min(20, maxFrames-15)
			if !bytes.Equal(segment.WAV[44:44+preFrames*captureFrameBytes], make([]byte, preFrames*captureFrameBytes)) {
				t.Fatal("incorrect trimmed pre-roll")
			}
			speechPCM := segment.WAV[44+preFrames*captureFrameBytes:]
			if len(speechPCM) < 15*captureFrameBytes {
				t.Fatalf("only %d speech bytes", len(speechPCM))
			}
			for i := 0; i < len(speechPCM); i += 2 {
				if binary.LittleEndian.Uint16(speechPCM[i:]) != 4000 {
					t.Fatalf("speech lost at sample %d", i/2)
				}
			}
		})
	}
}

func TestCaptureStopAndCancellation(t *testing.T) {
	for _, n := range []int{14, 15} {
		s := captureTestConfig()
		// Drive stop only after all frames were consumed, using an unbuffered
		// channel handshake (no timing dependence or real microphone).
		frames := make(chan captureFrame)
		stop := make(chan struct{})
		sent := make(chan struct{})
		go func() {
			defer close(sent)
			for _, f := range pcmFrames(n, 4000) {
				frames <- f
			}
			close(stop)
		}()
		count := 0
		err := captureFrames(context.Background(), s, false, stop, frames, func(seg Segment) error {
			count++
			if seg.Reason != "stop" {
				t.Fatal(seg.Reason)
			}
			return nil
		})
		<-sent
		want := 0
		if n == 15 {
			want = 1
		}
		if err != nil || count != want {
			t.Fatalf("n=%d count=%d err=%v", n, count, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := captureFrames(ctx, captureTestConfig(), false, nil, frameQueue(pcmFrames(20, 4000)), func(Segment) error { t.Fatal("emit on cancellation"); return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestCaptureInitialTimeoutAndCallbackError(t *testing.T) {
	s := captureTestConfig()
	s.StartTimeout = time.Millisecond
	if err := captureFrames(context.Background(), s, true, nil, make(chan captureFrame), func(Segment) error { t.Fatal("emit on initial timeout"); return nil }); err != nil {
		t.Fatal(err)
	}
	s.SilenceTimeout = time.Millisecond
	want := errors.New("queue full")
	err := captureFrames(context.Background(), s, false, nil, frameQueue(pcmFrames(15, 4000), pcmFrames(1, 0)), func(Segment) error { return want })
	if !errors.Is(err, want) {
		t.Fatal(err)
	}
}

func TestCaptureWallClockMaximumAndPartialCancellation(t *testing.T) {
	for _, cancelPartial := range []bool{false, true} {
		s := captureTestConfig()
		// Supply qualifying speech faster than realtime, then stop supplying
		// samples: a wall-clock limit must still endpoint this utterance.
		s.MaxDuration = time.Second
		frames := make(chan captureFrame)
		ctx, cancel := context.WithCancel(context.Background())
		sent := make(chan struct{})
		go func() {
			defer close(sent)
			for _, f := range pcmFrames(15, 4000) {
				frames <- f
			}
			if cancelPartial {
				cancel()
			}
		}()
		count := 0
		err := captureFrames(ctx, s, true, nil, frames, func(seg Segment) error {
			count++
			if seg.Reason != "max_duration" {
				t.Fatal(seg.Reason)
			}
			return nil
		})
		cancel()
		<-sent
		if cancelPartial {
			if !errors.Is(err, context.Canceled) || count != 0 {
				t.Fatalf("cancel: %v count %d", err, count)
			}
		} else if err != nil || count != 1 {
			t.Fatalf("wall maximum: %v count %d", err, count)
		}
	}
}

func TestCaptureStalledProcess(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	err := NewCapturer(CaptureConfig{Enabled: true, Executable: captureScript(t, "exec sleep 30\n")}).Capture(ctx, CaptureOptions{}, nil, func(Segment) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "no complete PCM frame for 5s") {
		t.Fatalf("stall: %v", err)
	}
}

func captureScript(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pw-record")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func capturePCMFile(t *testing.T, frames []captureFrame) string {
	t.Helper()
	var b bytes.Buffer
	for _, f := range frames {
		b.Write(f.data[:])
	}
	path := filepath.Join(t.TempDir(), "audio.raw")
	if err := os.WriteFile(path, b.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCaptureProcessContinuousArgv(t *testing.T) {
	pcm := capturePCMFile(t, append(append(pcmFrames(15, 4000), pcmFrames(2, 0)...), append(pcmFrames(15, 4000), pcmFrames(2, 0)...)...))
	args := filepath.Join(t.TempDir(), "args")
	calls := filepath.Join(t.TempDir(), "calls")
	exe := captureScript(t, fmt.Sprintf("printf '%%s\\n' \"$@\" > '%s'\necho call >> '%s'\ncat '%s'\nexec sleep 30\n", args, calls, pcm))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	stop := make(chan struct{})
	count := 0
	err := NewCapturer(CaptureConfig{Enabled: true, Executable: exe}).Capture(ctx, CaptureOptions{Target: "source;not shell", SilenceTimeout: 20 * time.Millisecond}, stop, func(s Segment) error {
		count++
		if count == 2 {
			close(stop)
		}
		return validateWAV(s.WAV, 5*time.Minute)
	})
	if err != nil || count != 2 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	got, _ := os.ReadFile(args)
	if string(got) != "--raw\n--format\ns16\n--rate\n16000\n--channels\n1\n--target\nsource;not shell\n-\n" {
		t.Fatalf("argv: %q", got)
	}
	got, _ = os.ReadFile(calls)
	if string(got) != "call\n" {
		t.Fatalf("recorder restarted: %q", got)
	}
}

func TestCaptureProcessFailures(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{"eof", "exit 0\n", "stream ended"},
		{"partial", "printf x\n", "stream ended"},
		{"failure", "exit 42\n", "stream ended"},
		{"stderr overflow", "head -c 70000 /dev/zero >&2\nexec sleep 30\n", "stderr exceeds"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pidPath := filepath.Join(t.TempDir(), "child.pid")
			body := fmt.Sprintf("sleep 30 &\necho $! > '%s'\n", pidPath) + tc.body
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			err := NewCapturer(CaptureConfig{Enabled: true, Executable: captureScript(t, body)}).Capture(ctx, CaptureOptions{}, nil, func(Segment) error { t.Fatal("unexpected emit"); return nil })
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %s", err, tc.want)
			}
			data, readErr := os.ReadFile(pidPath)
			if readErr != nil {
				t.Fatal(readErr)
			}
			pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data)))
			if parseErr != nil {
				t.Fatal(parseErr)
			}
			assertCaptureProcessGone(t, pid)
		})
	}
}

func TestCaptureCancellationKillsProcessGroup(t *testing.T) {
	pidPath := filepath.Join(t.TempDir(), "child.pid")
	exe := captureScript(t, fmt.Sprintf("sleep 30 &\necho $! > '%s'\nwait\n", pidPath))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- NewCapturer(CaptureConfig{Enabled: true, Executable: exe}).Capture(ctx, CaptureOptions{}, nil, func(Segment) error { return nil })
	}()
	data := waitForFile(t, pidPath, func(b []byte) bool { return len(b) > 0 })
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("capture hung")
	}
	assertCaptureProcessGone(t, pid)
}

func assertCaptureProcessGone(t *testing.T, pid int) {
	t.Helper()
	// SIGKILL can take a scheduler turn to complete; a killed grandchild can
	// remain a zombie until init reaps it. Neither state is a live recorder.
	until := time.Now().Add(time.Second)
	for {
		stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if os.IsNotExist(err) || errors.Is(err, syscall.ESRCH) || (err == nil && strings.Contains(string(stat), ") Z ")) {
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		if time.Now().After(until) {
			t.Fatalf("descendant still running: %s", stat)
		}
		time.Sleep(time.Millisecond)
	}
}
