package sherpavoice

import (
	"errors"
	"sync/atomic"
	"testing"
)

func TestWakeGatedAmbientSpeechNeverRunsASR(t *testing.T) {
	var calls atomic.Int32
	f := &fakeFront{}
	e, events := newTestEngine(t, f, &fakeASR{decode: func([]float32) (string, error) { calls.Add(1); return "ambient", nil }})
	for i := 0; i < 100; i++ {
		start := int64(i * 16000)
		f.frames = []fakeFrame{{activity: Activity{Speaking: true}}, {activity: Activity{Segments: []Span{{start, start + 16000}}}}}
		feed(t, e, 2)
	}
	f.flush = Activity{Segments: []Span{{e.ring.end - 8000, e.ring.end}}}
	if err := e.Stop(); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 || count(*events, "caption") != 0 || count(*events, "note_candidate") != 0 {
		t.Fatalf("ambient ASR calls=%d events=%v", calls.Load(), *events)
	}
	if count(*events, "activity") == 0 {
		t.Fatal("level/state activity disappeared")
	}
}

func TestWakeGatedFinalPreservesPrerollWithoutDuplicateDecode(t *testing.T) {
	var calls atomic.Int32
	f := &fakeFront{frames: []fakeFrame{{activity: Activity{Speaking: true}}, {wakes: []Wake{{Phrase: "hey ginkgo", Span: Span{9000, 16000}}}, activity: Activity{Speaking: true}}, {activity: Activity{Segments: []Span{{0, 24000}}}}}}
	e, events := newTestEngine(t, f, &fakeASR{decode: func(p []float32) (string, error) {
		calls.Add(1)
		if len(p) != 23000 {
			return "", errors.New("pre-roll was lost")
		}
		return "hey ginkgo the command", nil
	}})
	feed(t, e, 3)
	if err := e.Stop(); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || count(*events, "note_candidate") != 1 {
		t.Fatalf("calls=%d events=%v", calls.Load(), *events)
	}
}

func TestSelectedPreviewFinalAndReturnToIdle(t *testing.T) {
	var calls atomic.Int32
	f := &fakeFront{frames: []fakeFrame{{wakes: []Wake{{Phrase: "hey ginkgo", Span: Span{0, 8000}}}, activity: Activity{Speaking: true}}, {activity: Activity{Speaking: true}}}}
	e, events := newTestEngine(t, f, &fakeASR{decode: func([]float32) (string, error) { calls.Add(1); return "hey ginkgo selected dictation", nil }})
	feed(t, e, 2)
	s := e.gate
	if s == nil {
		t.Fatal("missing selection")
	}
	if !s.previewPublished {
		if err := e.result(<-e.results); err != nil {
			t.Fatal(err)
		}
	}
	if !s.previewPublished {
		t.Fatal("missing live preview")
	}
	f.frames = []fakeFrame{{activity: Activity{Segments: []Span{{0, 24000}}}}}
	feed(t, e, 1)
	for len(e.finals) > 0 {
		if err := e.result(<-e.results); err != nil {
			t.Fatal(err)
		}
	}
	if e.gate != nil {
		t.Fatal("selection did not complete")
	}
	before := len(*events)
	if err := e.result(decodeResult{job: decodeJob{preview: s}, text: "late preview", err: errors.New("stale failure")}); err != nil {
		t.Fatal(err)
	}
	if len(*events) != before {
		t.Fatal("late preview published")
	}
	f.frames = []fakeFrame{{activity: Activity{Speaking: true}}, {activity: Activity{Segments: []Span{{24000, 40000}}}}}
	feed(t, e, 2)
	if err := e.Stop(); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("expected one preview and one final decode, got %d", calls.Load())
	}
	var captions []Event
	for _, event := range *events {
		if event.Type == "caption" {
			captions = append(captions, event)
		}
	}
	if len(captions) != 2 || captions[0].Final || !captions[1].Final || captions[0].ID != captions[1].ID || captions[0].Revision >= captions[1].Revision {
		t.Fatalf("invalid preview lifecycle: %+v", captions)
	}
}
