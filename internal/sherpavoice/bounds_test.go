package sherpavoice

import (
	"bytes"
	"io"
	"testing"

	"github.com/mithrel/ginkgo/internal/voiceworker"
)

func TestSelectedDurationCapRetainsWakeIdentity(t *testing.T) {
	f := &fakeFront{frames: []fakeFrame{{wakes: []Wake{{Phrase: "wake", Span: Span{0, 8000}}}, activity: Activity{Speaking: true}}, {activity: Activity{Speaking: true}}}}
	c := DefaultEngineConfig()
	c.MaxRecordingSamples = 16000
	var events []Event
	e, err := NewEngine(c, f, &fakeASR{text: "wake dictation"}, func(v Event) error { events = append(events, v); return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer e.Cancel()
	feed(t, e, 2)
	if err = e.Stop(); err != nil {
		t.Fatal(err)
	}
	var wake, candidate Event
	for _, v := range events {
		if v.Type == "wake" {
			wake = v
		}
		if v.Type == "note_candidate" {
			candidate = v
		}
	}
	if candidate.ID != wake.ID || candidate.Start != wake.Start || candidate.End != 16000 || candidate.End < wake.End {
		t.Fatalf("wake=%+v candidate=%+v", wake, candidate)
	}
}
func TestSlowASRDoesNotBlockFrontendAndFinalOverflowFaults(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	r := &fakeASR{decode: func([]float32) (string, error) {
		select {
		case <-started:
		default:
			close(started)
		}
		<-release
		return "text", nil
	}}
	f := &fakeFront{frames: []fakeFrame{{activity: Activity{Segments: []Span{{0, 8000}}}}, {activity: Activity{Segments: []Span{{8000, 16000}}}}, {activity: Activity{Segments: []Span{{16000, 24000}}}}}}
	c := DefaultEngineConfig()
	c.FinalQueue = 1
	e, err := NewEngine(c, f, r, func(Event) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	feed(t, e, 1)
	<-started
	feed(t, e, 1)
	if err = e.Process(make([]float32, 8000)); err == nil {
		t.Fatal("final overflow silently dropped")
	}
	close(release)
	e.Cancel()
}
func TestRecordingOptOutStillCaptions(t *testing.T) {
	f := &fakeFront{frames: []fakeFrame{{wakes: []Wake{{Phrase: "wake", Span: Span{0, 8000}}}, activity: Activity{Segments: []Span{{0, 8000}}}}}}
	c := DefaultEngineConfig()
	c.RecordingEnabled = false
	var events []Event
	e, err := NewEngine(c, f, &fakeASR{text: "wake captions only"}, func(v Event) error { events = append(events, v); return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer e.Cancel()
	feed(t, e, 1)
	if err = e.Stop(); err != nil {
		t.Fatal(err)
	}
	if count(events, "wake") != 0 || count(events, "note_candidate") != 0 || count(events, "caption") != 1 {
		t.Fatalf("events: %+v", events)
	}
}

type blockedWriter struct{ started, release chan struct{} }

func (w blockedWriter) Write(b []byte) (int, error) {
	select {
	case <-w.started:
	default:
		close(w.started)
	}
	<-w.release
	return len(b), nil
}
func TestStdoutBackpressureIsBounded(t *testing.T) {
	c := wireConfig()
	blocked := blockedWriter{make(chan struct{}), make(chan struct{})}
	w := newEventWriter(blocked, "test-epoch", c)
	if err := w.send(voiceworker.Event{Type: "ready", Provider: &voiceworker.ProviderStatus{Requested: voiceworker.Auto, Effective: voiceworker.CPU, FallbackReason: "cuda_unavailable"}}); err != nil {
		t.Fatal(err)
	}
	<-blocked.started
	level := 0.1
	for i := 0; i < 8; i++ {
		if err := w.send(voiceworker.Event{Type: "level", Level: &level}); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.send(voiceworker.Event{Type: "level", Level: &level}); err == nil {
		t.Fatal("output overflow ignored")
	}
	close(blocked.release)
	if err := w.close(); err != nil {
		t.Fatal(err)
	}
}
func TestMalformedCommandsAndHelperLimits(t *testing.T) {
	c := wireConfig()
	c.MaxRecordingMS = 3600000
	var input, output bytes.Buffer
	sendCommand(t, &input, "init", &c)
	runtime := fakeRuntime(&fakeFront{}, &fakeASR{}, StartMicrophone)
	runtime.Open = func(NativeConfig) (Frontend, Recognizer, error) {
		t.Fatal("models initialized for unsupported bounds")
		return nil, nil, nil
	}
	if err := Serve(t.Context(), io.NopCloser(&input), &output, runtime); err == nil || output.Len() != 0 {
		t.Fatal("unsupported bounds accepted")
	}
}
