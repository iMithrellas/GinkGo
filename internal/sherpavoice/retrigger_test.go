package sherpavoice

import (
	"errors"
	"testing"
)

func TestRetriggerIgnoresSupersededMergedSnapshot(t *testing.T) {
	e, events := newTestEngine(t, &fakeFront{}, &fakeASR{})
	old := &selection{id: "old", utterance: "old-audio", phrase: "Hey Ginkgo", wakeEnd: 4000, closingEnd: 12000, pending: 2}
	e.gate = old
	e.deferred = &selection{id: "next", utterance: "next-audio", phrase: "Hey Ginkgo", label: "NOTE_ALIAS", start: 12000, wakeEnd: 24000, recording: true}
	if err := e.result(decodeResult{job: decodeJob{final: true, selection: old, span: Span{0, 32000}}, text: "Hey Ginkgo old command mixed with next wake"}); err != nil {
		t.Fatal(err)
	}
	if len(*events) != 0 {
		t.Fatal("superseded snapshot published")
	}
	if err := e.result(decodeResult{job: decodeJob{final: true, selection: old, span: Span{0, 12000}}, text: "Hey Ginkgo complete old command"}); err != nil {
		t.Fatal(err)
	}
	if count(*events, "note_candidate") != 1 || count(*events, "wake") != 1 || e.deferred != nil || e.gate.id != "next" {
		t.Fatal("bounded close did not hand off")
	}
}

func TestMergedVADRetriggerDefersUntilPriorFinal(t *testing.T) {
	for _, mode := range []string{"dictation", "control_only", "cancel", "stop", "two_notes", "timeout", "overflow"} {
		t.Run(mode, func(t *testing.T) {
			started, release := make(chan struct{}), make(chan struct{})
			blocked := false
			recognizer := &fakeASR{decode: func(p []float32) (string, error) {
				if len(p) == 12000 {
					if !blocked {
						blocked = true
						close(started)
						<-release
					}
					// Exact retained prefix stops BEFORE new wake context at12000.
					if p[0] != .1 || p[7999] != .1 || p[8000] != .2 || p[11999] != .2 {
						return "", errors.New("prior PCM changed")
					}
					if mode == "control_only" {
						return "Hey Ginkgo! Hey Ginko!", nil
					}
					return "Hey Ginkgo complete first command", nil
				}
				if len(p) == 20000 && (mode == "two_notes" || mode == "cancel") {
					return "Hey Ginkgo complete second command", nil
				}
				return "Hey Ginkgo", nil
			}}
			f := &fakeFront{frames: []fakeFrame{
				{wakes: []Wake{{Phrase: "hey ginkgo", Span: Span{0, 4000}}}, activity: Activity{Speaking: true}},
				{activity: Activity{Speaking: true}},
				{activity: Activity{Speaking: true}},
				{wakes: []Wake{{Phrase: "hey ginkgo", Span: Span{20000, 24000}}}, activity: Activity{Speaking: true}},
			}}
			c := DefaultEngineConfig()
			c.Cooldown = 0
			c.PreviewEvery = 1000000
			var events []Event
			e, err := NewEngine(c, f, recognizer, func(v Event) error { events = append(events, v); return nil })
			if err != nil {
				t.Fatal(err)
			}
			defer e.Cancel()
			for i := 0; i < 4; i++ {
				p := make([]float32, 8000)
				for j := range p {
					p[j] = float32(i+1) / 10
				}
				if err := e.Process(p); err != nil {
					t.Fatal(err)
				}
			}
			<-started
			if count(events, "wake") != 1 || e.deferred == nil || e.gate.recording {
				close(release)
				t.Fatal("new public authority before prior final")
			}
			if mode == "overflow" {
				f.frames = []fakeFrame{{wakes: []Wake{{Phrase: "hey ginkgo", Span: Span{30000, 36000}}}}}
				err := e.Process(make([]float32, 8000))
				close(release)
				if err == nil {
					t.Fatal("unbounded deferred wake")
				}
				e.Cancel()
				return
			}
			// Silero's merged endpoint contains BOTH wakes. Only the deferred source
			// selection receives its own suffix; the old close job cannot be replaced.
			f.frames = []fakeFrame{{activity: Activity{Segments: []Span{{0, 32000}}}}}
			if err := e.Process(make([]float32, 8000)); err != nil {
				close(release)
				t.Fatal(err)
			}
			if mode == "cancel" {
				close(release)
				e.Cancel()
				if count(events, "note_candidate") != 0 {
					t.Fatal("canceled retrigger saved")
				}
				return
			}
			if mode == "timeout" {
				for i := 0; i < 9; i++ {
					if err := e.Process(make([]float32, 8000)); err != nil {
						close(release)
						t.Fatal(err)
					}
				}
				if e.deferred == nil || e.deferred.recording {
					close(release)
					t.Fatal("deferred idle deadline stopped advancing")
				}
			}
			close(release)
			if mode != "stop" && mode != "two_notes" {
				for e.deferred != nil || e.gate != nil && e.gate.pending > 0 {
					r, ok := <-e.results
					if !ok {
						t.Fatal("worker ended")
					}
					if err := e.result(r); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := e.Stop(); err != nil {
				t.Fatal(err)
			}
			want := 1
			if mode == "two_notes" {
				want = 2
			}
			if mode == "control_only" {
				want = 0
			}
			if count(events, "note_candidate") != want || count(events, "wake") != 2 {
				t.Fatalf("notes/wakes: %+v", events)
			}
			priorTerminal, nextWake := -1, -1
			for i, v := range events {
				if v.Type == "note_candidate" && priorTerminal < 0 {
					if v.Start != 0 || v.End != 12000 || v.Original != "Hey Ginkgo complete first command" {
						t.Fatal("prior command mixed/truncated")
					}
					priorTerminal = i
				}
				if v.Type == "note_candidate" && v.Start == 12000 {
					if v.End != 32000 || v.Original != "Hey Ginkgo complete second command" {
						t.Fatal("new command changed")
					}
				}
				if v.Type == "note_canceled" && priorTerminal < 0 {
					priorTerminal = i
				}
				if v.Type == "wake" && v.Start == 12000 {
					nextWake = i
				}
			}
			if priorTerminal < 0 || nextWake <= priorTerminal {
				t.Fatal("singleton handoff event order")
			}
		})
	}
}
