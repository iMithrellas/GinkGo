package sherpavoice

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mithrel/ginkgo/internal/voiceworker"
)

func wireConfig() voiceworker.Config {
	g := voiceworker.Graph{Encoder: "/models/encoder.onnx", Decoder: "/models/decoder.onnx", Joiner: "/models/joiner.onnx", Tokens: "/models/tokens.txt"}
	return voiceworker.Config{Enabled: true, RecorderExecutable: "/usr/bin/pw-record", Parakeet: g, KWS: g, KeywordsFile: "/models/keywords.txt", SileroModel: "/models/silero.onnx", Provider: voiceworker.Auto, AllowCPUFallback: true, RecordingEnabled: true, UpdateMS: 32, WindowMS: 5000, SilenceMS: 500, MaxRecordingMS: 30000, TriggerTimeoutMS: 5000, CooldownMS: 2000, PreRollMS: 500}
}

type wireSink struct {
	validator *voiceworker.Validator
	events    chan voiceworker.Event
}

func (s *wireSink) Write(b []byte) (int, error) {
	e, err := s.validator.Decode(bytes.TrimSpace(b))
	if err != nil {
		return 0, err
	}
	s.events <- e
	return len(b), nil
}
func waitWire(t *testing.T, s *wireSink, kind string) voiceworker.Event {
	t.Helper()
	for {
		select {
		case e := <-s.events:
			if e.Type == kind {
				return e
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("missing %s", kind)
			return voiceworker.Event{}
		}
	}
}
func fakeRuntime(f *fakeFront, r Recognizer, mic func(string, string) (PCMSource, error)) Runtime {
	return Runtime{Probe: func(p string) (ProviderStatus, error) { return SelectProvider(p, false) }, Open: func(NativeConfig) (Frontend, Recognizer, error) { return f, r, nil }, Microphone: mic}
}
func sendCommand(t *testing.T, w io.Writer, kind string, c *voiceworker.Config) {
	t.Helper()
	if err := json.NewEncoder(w).Encode(voiceworker.Command{Version: 1, Type: kind, Epoch: "test-epoch", Config: c}); err != nil {
		t.Fatal(err)
	}
}

func TestServeNoMicrophoneBeforeStart(t *testing.T) {
	c := wireConfig()
	in, parent := io.Pipe()
	sink := &wireSink{validator: voiceworker.NewValidator("test-epoch", c), events: make(chan voiceworker.Event, 32)}
	var opened atomic.Bool
	source := &blockingSource{closed: make(chan struct{})}
	runtime := fakeRuntime(&fakeFront{}, &fakeASR{}, func(string, string) (PCMSource, error) { opened.Store(true); return source, nil })
	done := make(chan error, 1)
	go func() { done <- Serve(context.Background(), in, sink, runtime) }()
	sendCommand(t, parent, "init", &c)
	ready := waitWire(t, sink, "ready")
	if opened.Load() {
		t.Fatal("microphone before start")
	}
	if ready.Provider.Effective != voiceworker.CPU || ready.Provider.FallbackReason != "cuda_unavailable" {
		t.Fatalf("provider: %+v", ready.Provider)
	}
	sendCommand(t, parent, "start", nil)
	sendCommand(t, parent, "stop", nil)
	_ = parent.Close()
	waitWire(t, sink, "stopped")
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !opened.Load() {
		t.Fatal("missing explicit start capture")
	}
	select {
	case <-source.closed:
	default:
		t.Fatal("source not cleaned")
	}
}
func TestServeStopBeforeStart(t *testing.T) {
	c := wireConfig()
	var opened bool
	runtime := fakeRuntime(&fakeFront{}, &fakeASR{}, func(string, string) (PCMSource, error) {
		opened = true
		return nil, errors.New("unexpected microphone")
	})
	var input, output bytes.Buffer
	sendCommand(t, &input, "init", &c)
	sendCommand(t, &input, "stop", nil)
	if err := Serve(context.Background(), io.NopCloser(&input), &output, runtime); err != nil {
		t.Fatal(err)
	}
	if opened {
		t.Fatal("stop opened microphone")
	}
	v := voiceworker.NewValidator("test-epoch", c)
	for _, line := range bytes.Split(bytes.TrimSpace(output.Bytes()), []byte{'\n'}) {
		if _, err := v.Decode(line); err != nil {
			t.Fatal(err)
		}
	}
}
func TestServeSelectedCandidateProvenance(t *testing.T) {
	c := wireConfig()
	in, parent := io.Pipe()
	sink := &wireSink{validator: voiceworker.NewValidator("test-epoch", c), events: make(chan voiceworker.Event, 64)}
	pcm, feed := io.Pipe()
	f := &fakeFront{frames: []fakeFrame{{wakes: []Wake{{Phrase: "hey ginkgo", Span: Span{0, 512}}}, activity: Activity{Speaking: true}}}, flush: Activity{Segments: []Span{{0, 4096}}}}
	runtime := fakeRuntime(f, &fakeASR{text: "hey ginkgo, exact dictated text"}, func(string, string) (PCMSource, error) { return pcm, nil })
	done := make(chan error, 1)
	go func() { done <- Serve(context.Background(), in, sink, runtime) }()
	sendCommand(t, parent, "init", &c)
	waitWire(t, sink, "ready")
	sendCommand(t, parent, "start", nil)
	for i := 0; i < 8; i++ {
		if _, err := feed.Write(make([]byte, 1024)); err != nil {
			t.Fatal(err)
		}
		waitWire(t, sink, "level")
	}
	sendCommand(t, parent, "stop", nil)
	_ = parent.Close()
	candidate := waitWire(t, sink, "note_candidate")
	waitWire(t, sink, "stopped")
	_ = parent.Close()
	_ = feed.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if candidate.Candidate.Original != "hey ginkgo, exact dictated text" || candidate.Candidate.Body != candidate.Candidate.Original || candidate.Candidate.Audio.Start != 0 || candidate.Candidate.Audio.End != 4096 || candidate.Candidate.Control != nil {
		t.Fatalf("provenance: %+v", candidate.Candidate)
	}
}
func TestServeInitializationFailureHasNoEvents(t *testing.T) {
	c := wireConfig()
	for _, probeFailure := range []bool{true, false} {
		t.Run(map[bool]string{true: "probe", false: "constructor"}[probeFailure], func(t *testing.T) {
			var input, output bytes.Buffer
			sendCommand(t, &input, "init", &c)
			runtime := fakeRuntime(&fakeFront{}, &fakeASR{}, nil)
			runtime.Microphone = func(string, string) (PCMSource, error) { t.Fatal("microphone on init failure"); return nil, nil }
			if probeFailure {
				runtime.Probe = func(string) (ProviderStatus, error) { return ProviderStatus{}, ErrUnsupported }
			} else {
				runtime.Open = func(NativeConfig) (Frontend, Recognizer, error) {
					return nil, nil, errors.New("native constructor failed")
				}
			}
			if err := Serve(context.Background(), io.NopCloser(&input), &output, runtime); err == nil {
				t.Fatal("missing failure")
			}
			if output.Len() != 0 {
				t.Fatalf("pre-ready output: %s", output.Bytes())
			}
		})
	}
}
func TestServeFreshCPUAttemptAndForbiddenFallback(t *testing.T) {
	c := wireConfig()
	c.AttemptProvider = voiceworker.CPU
	c.FallbackReason = "cuda_init_failed"
	var input, output bytes.Buffer
	sendCommand(t, &input, "init", &c)
	sendCommand(t, &input, "stop", nil)
	runtime := fakeRuntime(&fakeFront{}, &fakeASR{}, func(string, string) (PCMSource, error) { t.Fatal("capture before start"); return nil, nil })
	baseOpen := runtime.Open
	runtime.Open = func(n NativeConfig) (Frontend, Recognizer, error) {
		if n.Provider != "cpu" {
			t.Fatal("ignored CPU retry")
		}
		return baseOpen(n)
	}
	if err := Serve(context.Background(), io.NopCloser(&input), &output, runtime); err != nil {
		t.Fatal(err)
	}
	first := bytes.Split(output.Bytes(), []byte{'\n'})[0]
	v := voiceworker.NewValidator("test-epoch", c)
	e, err := v.Decode(first)
	if err != nil || e.Provider.FallbackReason != "cuda_init_failed" {
		t.Fatalf("%+v %v", e, err)
	}
	c = wireConfig()
	c.AllowCPUFallback = false
	input.Reset()
	output.Reset()
	sendCommand(t, &input, "init", &c)
	if err := Serve(context.Background(), io.NopCloser(&input), &output, runtime); err == nil || output.Len() != 0 {
		t.Fatal("forbidden CPU fallback")
	}
}
func TestServeUnexpectedEOFAndDuplicateStart(t *testing.T) {
	for _, duplicate := range []bool{false, true} {
		t.Run(map[bool]string{true: "duplicate", false: "eof"}[duplicate], func(t *testing.T) {
			c := wireConfig()
			in, parent := io.Pipe()
			sink := &wireSink{validator: voiceworker.NewValidator("test-epoch", c), events: make(chan voiceworker.Event, 32)}
			source := &blockingSource{closed: make(chan struct{})}
			runtime := fakeRuntime(&fakeFront{}, &fakeASR{}, func(string, string) (PCMSource, error) { return source, nil })
			done := make(chan error, 1)
			go func() { done <- Serve(context.Background(), in, sink, runtime) }()
			sendCommand(t, parent, "init", &c)
			waitWire(t, sink, "ready")
			sendCommand(t, parent, "start", nil)
			if duplicate {
				sendCommand(t, parent, "start", nil)
			}
			_ = parent.Close()
			if err := <-done; err == nil {
				t.Fatal("unexpected control accepted")
			}
			select {
			case <-source.closed:
			default:
				t.Fatal("recorder not closed")
			}
		})
	}
}
func TestWriterBoundsAndBodyContract(t *testing.T) {
	c := wireConfig()
	var output bytes.Buffer
	w := newEventWriter(&output, "test-epoch", c)
	if err := w.send(voiceworker.Event{Type: "ready", Provider: &voiceworker.ProviderStatus{Requested: voiceworker.Auto, Effective: voiceworker.CPU, FallbackReason: "cuda_unavailable"}}); err != nil {
		t.Fatal(err)
	}
	if err := w.domain(Event{Type: "caption", ID: "caption-1", Revision: 1, Span: Span{0, 16000}, Original: strings.Repeat("x", 16385)}); err == nil {
		t.Fatal("oversized text accepted")
	}
	_ = w.close()
}
