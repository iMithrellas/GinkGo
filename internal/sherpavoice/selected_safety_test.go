package sherpavoice

import (
	"errors"
	"strings"
	"testing"
)

func TestSelectedASRFailureCannotBecomeCandidate(t *testing.T) {
	f := &fakeFront{frames: []fakeFrame{{wakes: []Wake{{Phrase: "hey ginkgo", Span: Span{0, 8000}}}, activity: Activity{Segments: []Span{{0, 8000}}}}}}
	e, events := newTestEngine(t, f, &fakeASR{decode: func([]float32) (string, error) { return "Hey Ginkgo unsafe partial result", errors.New("ASR failure") }})
	err := e.Process(make([]float32, 8000))
	if err == nil {
		err = e.Stop()
	}
	if err == nil || count(*events, "note_candidate") != 0 {
		t.Fatal("failed ASR authorized note")
	}
}

func TestDiscardedVADPulseCannotKeepWakeArmed(t *testing.T) {
	f := &fakeFront{frames: []fakeFrame{{wakes: []Wake{{Phrase: "hey ginkgo", Span: Span{0, 8000}}}, activity: Activity{Speaking: true}}, {activity: Activity{}}}}
	e, events := newTestEngine(t, f, &fakeASR{text: "Hey Ginkgo"})
	feed(t, e, 9)
	if e.gate != nil || count(*events, "note_candidate") != 0 {
		t.Fatal("short VAD pulse stranded selection")
	}
}

func TestSelectedLeadingControlClassification(t *testing.T) {
	for _, c := range []struct {
		text, body string
		leading    bool
	}{
		{"Hey Ginkgo, exact dictation.", "exact dictation.", true},
		{"Hey Ginko. Exact dictation.", "Exact dictation.", true},
		{"Hey Ginkgo! Hey Ginko? Hey Ginkgo...", "", true},
		{"Hey Ginkgo...", "", true},
		{"He ginkgo?", "He ginkgo?", false},
		{"Heginko?", "Heginko?", false},
		{"Higgin Coke.", "Higgin Coke.", false},
		{"NOTE_ALIAS actual text", "NOTE_ALIAS actual text", false},
		{"previous ambient. Hey Ginkgo, dictation", "previous ambient. Hey Ginkgo, dictation", false},
		{"Hey Google, ginkgo biloba", "Hey Google, ginkgo biloba", false},
		{"Hey bingo", "Hey bingo", false},
		{"Hey Ginkgology text", "Hey Ginkgology text", false},
	} {
		body, ok := selectedBody(c.text, "Hey Ginkgo")
		if body != c.body || ok != c.leading {
			t.Fatalf("classification %q: %q,%v", c.text, body, ok)
		}
	}
}

func TestSelectedPrefixMissNeverFallsBackToOriginal(t *testing.T) {
	for _, text := range []string{"ambient text before Hey Ginkgo exact dictation", "NOTE_ALIAS dictation", "unknown words", "Hey Ginkgo! Hey Ginko!"} {
		f := &fakeFront{frames: []fakeFrame{{wakes: []Wake{{Phrase: "hey ginkgo", Span: Span{0, 8000}}}, activity: Activity{Segments: []Span{{0, 8000}}}}}}
		e, events := newTestEngine(t, f, &fakeASR{text: text})
		feed(t, e, 1)
		if err := e.Stop(); err != nil {
			t.Fatal(err)
		}
		if count(*events, "note_candidate") != 0 {
			t.Fatal("non-dictation saved")
		}
	}
}

func TestIdleTimeoutDoesNotSelectLaterAmbient(t *testing.T) {
	f := &fakeFront{frames: []fakeFrame{{wakes: []Wake{{Phrase: "hey ginkgo", Span: Span{0, 8000}}}, activity: Activity{Segments: []Span{{0, 8000}}}}}}
	r := &fakeASR{text: "Hey Ginkgo"}
	e, events := newTestEngine(t, f, r)
	feed(t, e, 1)
	for e.gate != nil && e.gate.pending > 0 {
		if err := e.result(<-e.results); err != nil {
			t.Fatal(err)
		}
	}
	// More than three audio seconds, independent of ASR return or wall time.
	feed(t, e, 7)
	r.text = "Hey Ginkgo untriggered ambient words"
	f.frames = []fakeFrame{{activity: Activity{Speaking: true}}, {activity: Activity{Segments: []Span{{64000, 80000}}}}}
	feed(t, e, 2)
	if err := e.Stop(); err != nil {
		t.Fatal(err)
	}
	if count(*events, "note_candidate") != 0 {
		t.Fatal("stale wake selected ambient")
	}
}

func TestRearmCancelsUnqualifiedOldSelectedResult(t *testing.T) {
	f := &fakeFront{}
	e, events := newTestEngine(t, f, &fakeASR{})
	// No qualifying VAD evidence or retained pre-retrigger interval exists.
	// Such a callback must be revoked, not borrowed by the new session. A
	// qualified prior command instead follows the bounded deferred handoff.
	old := &selection{id: "old", utterance: "old-audio", phrase: "Hey Ginkgo", pending: 1, recording: true}
	e.gate = old
	f.frames = []fakeFrame{{wakes: []Wake{{Phrase: "hey ginkgo", Span: Span{1000, 8000}}}}}
	feed(t, e, 1)
	if !old.decided || e.gate == nil || e.gate == old {
		t.Fatal("old selection not revoked")
	}
	if err := e.result(decodeResult{job: decodeJob{final: true, selection: old}, text: "Hey Ginkgo stale dictation"}); err != nil {
		t.Fatal(err)
	}
	if count(*events, "note_candidate") != 0 {
		t.Fatal("stale ASR result published")
	}
	if count(*events, "note_canceled") != 1 {
		t.Fatal("missing rearm cancellation")
	}
}

func TestPreRollEvictionRejectsRatherThanClips(t *testing.T) {
	f := &fakeFront{}
	c := DefaultEngineConfig()
	c.RingSamples = 16000
	c.PreviewSamples = 8000
	c.MaxRecordingSamples = 16000
	var events []Event
	e, err := NewEngine(c, f, &fakeASR{}, func(v Event) error { events = append(events, v); return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer e.Cancel()
	feed(t, e, 4) // retained [16000,32000)
	f.frames = []fakeFrame{{wakes: []Wake{{Phrase: "hey ginkgo", Span: Span{25000, 32000}}}}}
	feed(t, e, 1) // retained [24000,40000), requested pre-roll starts17000
	if count(events, "wake") != 0 || e.gate != nil {
		t.Fatal("evicted pre-roll was silently clipped")
	}
}

func TestContinuousDictationCanExceedIdleTimeout(t *testing.T) {
	f := &fakeFront{frames: []fakeFrame{{wakes: []Wake{{Phrase: "hey ginkgo", Span: Span{0, 8000}}}, activity: Activity{Speaking: true}}}}
	e, events := newTestEngine(t, f, &fakeASR{text: "Hey Ginkgo complete continuous command"})
	feed(t, e, 1)
	for i := 0; i < 8; i++ {
		f.frames = []fakeFrame{{activity: Activity{Speaking: true}}}
		feed(t, e, 1)
	}
	f.frames = []fakeFrame{{activity: Activity{Segments: []Span{{0, 80000}}}}}
	feed(t, e, 1)
	if err := e.Stop(); err != nil {
		t.Fatal(err)
	}
	if count(*events, "note_candidate") != 1 {
		t.Fatal("continuous command expired while speaking")
	}
	for _, v := range *events {
		if v.Type == "note_candidate" && !strings.HasSuffix(v.Original, "complete continuous command") {
			t.Fatal("command truncated")
		}
	}
}
