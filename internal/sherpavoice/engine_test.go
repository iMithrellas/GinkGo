package sherpavoice

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"
)

type fakeFrame struct {
	wakes    []Wake
	activity Activity
}
type fakeFront struct {
	frames []fakeFrame
	flush  Activity
	closed bool
}

func (f *fakeFront) Accept([]float32) ([]Wake, Activity, error) {
	if len(f.frames) == 0 {
		return nil, Activity{}, nil
	}
	v := f.frames[0]
	f.frames = f.frames[1:]
	// Explicit fixture-only canonical mapping. Production must never assume
	// result.keyword is spoken text; arbitrary aliases stay unknown here too.
	for i := range v.wakes {
		if v.wakes[i].SpokenForm == "" {
			switch v.wakes[i].Phrase {
			case "hey ginkgo":
				v.wakes[i].SpokenForm = "hey ginkgo"
			case "wake":
				v.wakes[i].SpokenForm = "wake"
			}
		}
	}
	return v.wakes, v.activity, nil
}
func (f *fakeFront) Flush() (Activity, error) { return f.flush, nil }
func (f *fakeFront) Close()                   { f.closed = true }

type fakeASR struct {
	text   string
	decode func([]float32) (string, error)
	closed chan struct{}
}

func (r *fakeASR) Decode(p []float32) (string, error) {
	if r.decode != nil {
		return r.decode(p)
	}
	return r.text, nil
}
func (r *fakeASR) Close() {
	if r.closed != nil {
		close(r.closed)
	}
}
func newTestEngine(t *testing.T, f *fakeFront, r Recognizer) (*Engine, *[]Event) {
	t.Helper()
	var events []Event
	e, err := NewEngine(DefaultEngineConfig(), f, r, func(v Event) error { events = append(events, v); return nil })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.Cancel)
	return e, &events
}
func count(events []Event, kind string) int {
	n := 0
	for _, v := range events {
		if v.Type == kind {
			n++
		}
	}
	return n
}
func feed(t *testing.T, e *Engine, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if err := e.Process(make([]float32, 8000)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCaptionCannotWakeOrSelect(t *testing.T) {
	f := &fakeFront{frames: []fakeFrame{{activity: Activity{Speaking: true}}, {activity: Activity{Segments: []Span{{0, 16000}}}}}}
	e, events := newTestEngine(t, f, &fakeASR{text: "hey ginkgo an ambient caption"})
	feed(t, e, 2)
	if err := e.Stop(); err != nil {
		t.Fatal(err)
	}
	if count(*events, "caption") != 1 || count(*events, "wake") != 0 || count(*events, "note_candidate") != 0 {
		t.Fatalf("events: %+v", *events)
	}
}
func TestAcousticWakeSelectsSameBreathBoundary(t *testing.T) {
	f := &fakeFront{frames: []fakeFrame{{activity: Activity{Speaking: true}}, {wakes: []Wake{{Phrase: "hey ginkgo", Span: Span{9000, 16000}}}, activity: Activity{Speaking: true}}, {activity: Activity{Segments: []Span{{0, 24000}}}}}}
	e, events := newTestEngine(t, f, &fakeASR{text: "hey ginkgo, Keep Exact CAPITALS."})
	feed(t, e, 3)
	if err := e.Stop(); err != nil {
		t.Fatal(err)
	}
	if count(*events, "wake") != 1 || count(*events, "note_candidate") != 1 {
		t.Fatalf("events: %+v", *events)
	}
	for _, v := range *events {
		if v.Type == "note_candidate" {
			if v.Start != 1000 || v.End != 24000 || v.Original != "hey ginkgo, Keep Exact CAPITALS." || v.Body != "Keep Exact CAPITALS." || v.SessionID == "" {
				t.Fatalf("candidate: %+v", v)
			}
		}
	}
}
func TestPhraseAloneThenSpeechAndShortNoise(t *testing.T) {
	f := &fakeFront{frames: []fakeFrame{{wakes: []Wake{{Phrase: "hey ginkgo", Span: Span{0, 8000}}}, activity: Activity{Segments: []Span{{0, 8000}}}}, {activity: Activity{Segments: []Span{{8100, 8200}}}}, {activity: Activity{Segments: []Span{{16000, 24000}}}}}}
	r := &fakeASR{decode: func(p []float32) (string, error) {
		if len(p) == 8000 {
			return "hey ginkgo", nil
		}
		return "", nil
	}}
	e, events := newTestEngine(t, f, r)
	feed(t, e, 1)
	// Deterministically drain caption and selected trigger-only finals before
	// supplying later speech; no timers or real inference involved.
	for len(e.finals) > 0 {
		if err := e.result(<-e.results); err != nil {
			t.Fatal(err)
		}
	}
	r.decode = func([]float32) (string, error) { return "hey ginkgo actual dictation", nil }
	feed(t, e, 2)
	if err := e.Stop(); err != nil {
		t.Fatal(err)
	}
	if count(*events, "note_candidate") != 1 {
		t.Fatalf("events: %+v", *events)
	}
	for _, v := range *events {
		if v.Type == "note_candidate" && v.Start != 0 {
			t.Fatalf("noise selected: %+v", v)
		}
	}
}
func TestTriggerOnlyTimeoutHasNoCandidate(t *testing.T) {
	f := &fakeFront{frames: []fakeFrame{{wakes: []Wake{{Phrase: "hey ginkgo", Span: Span{0, 8000}}}, activity: Activity{Segments: []Span{{0, 8000}}}}}}
	e, events := newTestEngine(t, f, &fakeASR{text: "hey ginkgo"})
	feed(t, e, 12)
	if err := e.Stop(); err != nil {
		t.Fatal(err)
	}
	if count(*events, "note_candidate") != 0 || e.gate != nil {
		t.Fatalf("events: %+v", *events)
	}
}
func TestCooldownAndNoSpeech(t *testing.T) {
	f := &fakeFront{frames: []fakeFrame{{wakes: []Wake{{Phrase: "hey ginkgo", Span: Span{0, 8000}}}}, {wakes: []Wake{{Phrase: "hey ginkgo", Span: Span{8000, 16000}}}}}}
	e, events := newTestEngine(t, f, &fakeASR{text: "unused"})
	feed(t, e, 2)
	if err := e.Stop(); err != nil {
		t.Fatal(err)
	}
	if count(*events, "wake") != 1 || count(*events, "note_candidate") != 0 {
		t.Fatalf("events: %+v", *events)
	}
}
func TestPreviewBeforeSilenceAndNoStaleAfterFinal(t *testing.T) {
	f := &fakeFront{frames: []fakeFrame{{activity: Activity{Speaking: true}}, {activity: Activity{Speaking: true}}, {activity: Activity{Speaking: true}}, {activity: Activity{Segments: []Span{{0, 32000}}}}}}
	e, events := newTestEngine(t, f, &fakeASR{text: "caption"})
	feed(t, e, 2)
	if count(*events, "caption") == 0 {
		first := <-e.results
		if first.job.final {
			t.Fatal("expected preview")
		}
		if err := e.result(first); err != nil {
			t.Fatal(err)
		}
	}
	feed(t, e, 2)
	if err := e.Stop(); err != nil {
		t.Fatal(err)
	}
	var captions []Event
	for _, v := range *events {
		if v.Type == "caption" {
			captions = append(captions, v)
		}
	}
	if len(captions) < 2 || captions[0].Final || !captions[len(captions)-1].Final {
		t.Fatalf("captions: %+v", captions)
	}
	for i := 1; i < len(captions); i++ {
		if captions[i].ID != captions[0].ID || captions[i].Revision <= captions[i-1].Revision {
			t.Fatalf("revisions: %+v", captions)
		}
	}
}
func TestStopFlushAndCancelDiscard(t *testing.T) {
	for _, stop := range []bool{true, false} {
		t.Run(map[bool]string{true: "stop", false: "cancel"}[stop], func(t *testing.T) {
			f := &fakeFront{frames: []fakeFrame{{wakes: []Wake{{Phrase: "wake", Span: Span{0, 8000}}}, activity: Activity{Speaking: true}}}, flush: Activity{Segments: []Span{{0, 8000}}}}
			closed := make(chan struct{})
			e, events := newTestEngine(t, f, &fakeASR{text: "wake save me", closed: closed})
			feed(t, e, 1)
			if stop {
				if err := e.Stop(); err != nil {
					t.Fatal(err)
				}
			} else {
				e.Cancel()
			}
			<-closed
			if !f.closed {
				t.Fatal("frontend not closed")
			}
			want := 0
			if stop {
				want = 1
			}
			if count(*events, "note_candidate") != want {
				t.Fatalf("events: %+v", *events)
			}
		})
	}
}
func TestBoundedQueueAndOutput(t *testing.T) {
	q := newQueue(1)
	if err := q.put(decodeJob{id: "a", final: true}); err != nil {
		t.Fatal(err)
	}
	if err := q.put(decodeJob{id: "b", final: true}); err == nil {
		t.Fatal("missing overflow")
	}
	q = newQueue(2)
	_ = q.put(decodeJob{id: "a"})
	_ = q.put(decodeJob{id: "b"})
	j, ok := q.get()
	if !ok || j.id != "b" {
		t.Fatal("preview not coalesced")
	}
	q.close(true)
	f := &fakeFront{frames: []fakeFrame{{wakes: []Wake{{Phrase: "wake", Span: Span{0, 8000}}}}}}
	e, err := NewEngine(DefaultEngineConfig(), f, &fakeASR{}, func(Event) error { return errors.New("output overflow") })
	if err != nil {
		t.Fatal(err)
	}
	defer e.Cancel()
	if err = e.Process(make([]float32, 8000)); err == nil {
		t.Fatal("publisher backpressure ignored")
	}
}
func TestRingAndProvider(t *testing.T) {
	r := newRing(4)
	r.append([]float32{0, 1, 2})
	r.append([]float32{3, 4, 5})
	p, err := r.snapshot(Span{2, 6})
	if err != nil || !reflect.DeepEqual(p, []float32{2, 3, 4, 5}) {
		t.Fatalf("%v %v", p, err)
	}
	if _, err = r.snapshot(Span{1, 3}); err == nil {
		t.Fatal("missing ring boundary error")
	}
	for _, available := range []bool{false, true} {
		s, err := SelectProvider("auto", available)
		if err != nil {
			t.Fatal(err)
		}
		if (s.Actual == "cuda") != available {
			t.Fatalf("status: %+v", s)
		}
	}
	if _, err := SelectProvider("bad", true); err == nil {
		t.Fatal("invalid provider accepted")
	}
}
func TestStripLeadingPhrase(t *testing.T) {
	for _, c := range []struct{ in, want string }{{"Hey Ginkgo, Hello.", "Hello."}, {"hey ginkgo", ""}, {"not hey ginkgo", "not hey ginkgo"}, {"hey ginkgology", "hey ginkgology"}, {"hey ginkgo: keep hey ginkgo", "keep hey ginkgo"}} {
		if got := StripLeadingPhrase(c.in, "hey ginkgo"); got != c.want {
			t.Fatalf("%q => %q; want %q", c.in, got, c.want)
		}
	}
}

type blockingSource struct {
	closed chan struct{}
	once   sync.Once
}

func (s *blockingSource) Read([]byte) (int, error) { <-s.closed; return 0, io.EOF }
func (s *blockingSource) Close() error             { s.once.Do(func() { close(s.closed) }); return nil }
func TestCaptureCleanup(t *testing.T) {
	for _, stop := range []bool{true, false} {
		t.Run(map[bool]string{true: "stop", false: "cancel"}[stop], func(t *testing.T) {
			f := &fakeFront{}
			e, _ := newTestEngine(t, f, &fakeASR{})
			s := &blockingSource{closed: make(chan struct{})}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			stopCh := make(chan struct{})
			if stop {
				close(stopCh)
			} else {
				cancel()
			}
			err := Capture(ctx, stopCh, s, e)
			if stop && err != nil {
				t.Fatal(err)
			}
			if !stop && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			select {
			case <-s.closed:
			default:
				t.Fatal("source not closed")
			}
		})
	}
}
func TestRecorderEOFDiscards(t *testing.T) {
	f := &fakeFront{}
	e, events := newTestEngine(t, f, &fakeASR{})
	s := structSource{Reader: strings.NewReader("")}
	if err := Capture(context.Background(), make(chan struct{}), s, e); err == nil {
		t.Fatal("EOF accepted")
	}
	if count(*events, "note_candidate") != 0 {
		t.Fatal("EOF saved")
	}
}

type structSource struct{ io.Reader }

func (structSource) Close() error { return nil }
