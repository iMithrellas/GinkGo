package voice

import (
	"bytes"
	"errors"
	"testing"
	"time"
)

func testNoteSession(t *testing.T, c NoteSessionConfig) *NoteSession {
	t.Helper()
	n, err := NewNoteSession(c)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func feedNoteFrames(t *testing.T, n *NoteSession, now time.Time, count int) []byte {
	t.Helper()
	var pcm []byte
	for _, f := range pcmFrames(count, 4000) {
		pcm = append(pcm, f.data[:]...)
		if r, err := n.FeedPCM(now, PCMChunk{StartSample: n.next, PCM: f.data[:]}); err != nil || r != nil {
			t.Fatalf("feed: %v %+v", err, r)
		}
	}
	return pcm
}

func TestNoteDelayedTriggerAndDictationSameBreath(t *testing.T) {
	now := time.Unix(100, 0)
	n := testNoteSession(t, NoteSessionConfig{Enabled: true})
	background := feedNoteFrames(t, n, now, 20)
	phraseAndDictation := feedNoteFrames(t, n, now, 80)
	if n.State().RecordingNote || n.End(now) != nil {
		t.Fatal("background speech created a note")
	}
	ok, err := n.Trigger(now, TriggerDetection{StartSample: int64(len(background) / 2), EndSample: 60 * 160})
	if !ok || err != nil || !n.State().RecordingNote || n.State().SessionID != 1 {
		t.Fatalf("trigger: %t %v %+v", ok, err, n.State())
	}
	if n.ConfirmDictation(59*160) || !n.ConfirmDictation(60*160) {
		t.Fatal("dictation boundary")
	}
	r := n.End(now)
	if r == nil || r.State != "finalizing" || r.RecordingNote || !bytes.Equal(r.WAV[44:], phraseAndDictation) || r.EndSample != 16000 {
		t.Fatalf("final: %+v", r)
	}
	if n.End(now) != nil || n.Cancel(now) != nil {
		t.Fatal("duplicate final")
	}
}

func TestNoteTriggerOnlyTimeoutCooldownAndStaleDetections(t *testing.T) {
	now := time.Unix(100, 0)
	n := testNoteSession(t, NoteSessionConfig{Enabled: true})
	feedNoteFrames(t, n, now, 30)
	d := TriggerDetection{StartSample: 0, EndSample: 4800}
	if ok, err := n.Trigger(now, d); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if n.ConfirmDictation(4800) {
		t.Fatal("trigger alone counts as content")
	}
	if n.Tick(now.Add(5*time.Second-time.Nanosecond)) != nil {
		t.Fatal("early timeout")
	}
	r := n.Tick(now.Add(5 * time.Second))
	if r == nil || r.State != "canceled" || r.Reason != "trigger_timeout" || r.WAV != nil {
		t.Fatalf("timeout: %+v", r)
	}
	if !n.Rearm() {
		t.Fatal("rearm")
	}
	feedNoteFrames(t, n, now.Add(5*time.Second), 30)
	d = TriggerDetection{StartSample: 4800, EndSample: 9600}
	if ok, err := n.Trigger(now.Add(6*time.Second), d); ok || err != nil {
		t.Fatal("cooldown ignored", ok, err)
	}
	if ok, err := n.Trigger(now.Add(7*time.Second), TriggerDetection{StartSample: 0, EndSample: 4800}); ok || err != nil {
		t.Fatal("late duplicate accepted", ok, err)
	}
	if ok, err := n.Trigger(now.Add(7*time.Second), d); !ok || err != nil || n.State().SessionID != 2 {
		t.Fatal("new activation rejected", ok, err)
	}
}

func TestNoteManualCancelStopAndMaximum(t *testing.T) {
	now := time.Unix(100, 0)
	for _, action := range []string{"end", "cancel", "wall_max", "sample_max"} {
		t.Run(action, func(t *testing.T) {
			n := testNoteSession(t, NoteSessionConfig{Enabled: true, MaxDuration: 200 * time.Millisecond})
			if ok, err := n.Begin(now); !ok || err != nil {
				t.Fatal(ok, err)
			}
			if ok, _ := n.Begin(now); ok {
				t.Fatal("double begin")
			}
			pcm := feedNoteFrames(t, n, now, 15)
			if !n.ConfirmDictation(0) {
				t.Fatal("manual content")
			}
			var r *NoteSessionResult
			switch action {
			case "end":
				r = n.End(now)
			case "cancel":
				r = n.Cancel(now)
			case "wall_max":
				r = n.Tick(now.Add(200 * time.Millisecond))
			case "sample_max":
				chunk := pcmFrames(1, 5000)[0]
				var err error
				// A chunk straddling the cap is trimmed exactly; subsequent
				// samples remain in idle history, not in a second note.
				var tail []byte
				for i := 0; i < 10; i++ {
					tail = append(tail, chunk.data[:]...)
				}
				r, err = n.FeedPCM(now, PCMChunk{StartSample: n.next, PCM: tail})
				if err != nil {
					t.Fatal(err)
				}
				pcm = append(pcm, tail[:5*captureFrameBytes]...)
			}
			if r == nil || r.RecordingNote {
				t.Fatalf("result: %+v", r)
			}
			if action == "cancel" {
				if r.WAV != nil || r.State != "canceled" {
					t.Fatal("cancel kept audio")
				}
			} else if r.State != "finalizing" || !bytes.Equal(r.WAV[44:], pcm) {
				t.Fatal("final audio mismatch")
			}
			if action == "sample_max" && (r.EndSample != 3200 || n.next != 4000) {
				t.Fatal("sample maximum")
			}
			if !n.Rearm() {
				t.Fatal("rearm")
			}
			if ok, err := n.Begin(now); !ok || err != nil {
				t.Fatal("manual begin must override cooldown", ok, err)
			}
		})
	}
}

func TestNoteHistoryBoundsAndExpiredTrigger(t *testing.T) {
	now := time.Unix(100, 0)
	n := testNoteSession(t, NoteSessionConfig{Enabled: true, PreRoll: 200 * time.Millisecond})
	for i := 0; i < 1000; i++ {
		feedNoteFrames(t, n, now, 1)
	}
	if len(n.pre) != 6400 || cap(n.pre) != 6400 || n.audio != nil || n.State().State != "armed" {
		t.Fatal("idle memory bound")
	}
	if ok, err := n.Trigger(now, TriggerDetection{StartSample: 0, EndSample: 160}); ok || !errors.Is(err, ErrTriggerExpired) {
		t.Fatal("expired trigger silently truncated", ok, err)
	}
	oldest := n.next - int64(len(n.pre)/2)
	if ok, err := n.Trigger(now, TriggerDetection{StartSample: oldest, EndSample: oldest + 160}); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if len(n.audio) != 6400 || cap(n.audio) != 2*60*32000 {
		t.Fatal("active memory bound")
	}
}

func TestNoteDisabledAndInvalidInputIsolation(t *testing.T) {
	now := time.Unix(100, 0)
	n := testNoteSession(t, NoteSessionConfig{MaxDuration: -1})
	if _, err := n.FeedPCM(now, PCMChunk{}); !errors.Is(err, ErrDisabled) {
		t.Fatal(err)
	}
	if _, err := n.Trigger(now, TriggerDetection{StartSample: -1}); !errors.Is(err, ErrDisabled) {
		t.Fatal(err)
	}
	if _, err := n.Begin(now); !errors.Is(err, ErrDisabled) {
		t.Fatal(err)
	}
	if n.ConfirmDictation(0) || n.Rearm() || n.End(now) != nil || n.Cancel(now) != nil || n.Tick(now) != nil || n.State().State != "disabled" {
		t.Fatal("disabled side effect")
	}
	for _, c := range []NoteSessionConfig{{Enabled: true, PreRoll: -1}, {Enabled: true, PreRoll: time.Minute}, {Enabled: true, TriggerTimeout: -1}, {Enabled: true, Cooldown: -1}, {Enabled: true, MaxDuration: time.Hour}} {
		if _, err := NewNoteSession(c); err == nil {
			t.Fatal("accepted invalid config")
		}
	}
	n = testNoteSession(t, NoteSessionConfig{Enabled: true})
	for _, p := range []PCMChunk{{StartSample: 1, PCM: []byte{0, 0}}, {PCM: []byte{1}}, {PCM: make([]byte, 32002)}} {
		if _, err := n.FeedPCM(now, p); err == nil {
			t.Fatal("accepted invalid PCM")
		}
	}
	if n.next != 0 || len(n.pre) != 0 {
		t.Fatal("invalid input mutated clock")
	}
	if _, err := n.Trigger(now, TriggerDetection{EndSample: 1}); err == nil {
		t.Fatal("future detection accepted")
	}
}

func TestNoteManualDoesNotUseTriggerOnlyTimeout(t *testing.T) {
	now := time.Unix(100, 0)
	n := testNoteSession(t, NoteSessionConfig{Enabled: true})
	if ok, err := n.Begin(now); !ok || err != nil {
		t.Fatal(ok, err)
	}
	feedNoteFrames(t, n, now, 15)
	if n.Tick(now.Add(6*time.Second)) != nil || !n.State().RecordingNote {
		t.Fatal("manual session used trigger-only timeout")
	}
	if r := n.End(now.Add(6 * time.Second)); r == nil || r.WAV != nil || r.State != "canceled" {
		t.Fatal("manual session without verified content must not save")
	}
}
