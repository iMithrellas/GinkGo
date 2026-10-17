package sherpavoice

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"testing"

	"github.com/mithrel/ginkgo/internal/voiceworker"
)

func TestRejectEveryUnterminatedCommand(t *testing.T) {
	for _, kind := range []string{"init", "start", "stop"} {
		t.Run(kind, func(t *testing.T) {
			c := wireConfig()
			var input, output bytes.Buffer
			if kind != "init" {
				sendCommand(t, &input, "init", &c)
			}
			v := voiceworker.Command{Version: 1, Type: kind, Epoch: "test-epoch"}
			if kind == "init" {
				v.Config = &c
			}
			b, err := json.Marshal(v)
			if err != nil {
				t.Fatal(err)
			}
			input.Write(b)
			runtime := fakeRuntime(&fakeFront{}, &fakeASR{}, func(string, string) (PCMSource, error) { t.Fatal("partial command opened recorder"); return nil, nil })
			if err = Serve(t.Context(), io.NopCloser(&input), &output, runtime); err == nil {
				t.Fatalf("accepted unterminated %s", kind)
			}
			if bytes.Contains(output.Bytes(), []byte(`"type":"stopped"`)) {
				t.Fatal("partial EOF became graceful success")
			}
		})
	}
}

func TestDuplicateStopBeforeStartAborts(t *testing.T) {
	c := wireConfig()
	var input, output bytes.Buffer
	sendCommand(t, &input, "init", &c)
	sendCommand(t, &input, "stop", nil)
	sendCommand(t, &input, "stop", nil)
	runtime := fakeRuntime(&fakeFront{}, &fakeASR{}, func(string, string) (PCMSource, error) { t.Fatal("recorder opened"); return nil, nil })
	if err := Serve(t.Context(), io.NopCloser(&input), &output, runtime); err == nil {
		t.Fatal("duplicate stop accepted")
	}
	if bytes.Contains(output.Bytes(), []byte(`"type":"stopped"`)) {
		t.Fatal("invalid lifecycle emitted stopped")
	}
}

type flushSignalFrontend struct {
	*fakeFront
	flushed chan struct{}
}

func (f *flushSignalFrontend) Flush() (Activity, error) {
	a, err := f.fakeFront.Flush()
	close(f.flushed)
	return a, err
}

func TestDrainPublicationRequiresLiveContextAndStopEOF(t *testing.T) {
	for _, invalid := range []string{"cancel", "stop", "start", "partial"} {
		t.Run(invalid, func(t *testing.T) {
			c := wireConfig()
			in, parent := io.Pipe()
			pcm, feedPCM := io.Pipe()
			t.Cleanup(func() { _ = parent.Close(); _ = feedPCM.Close() })
			sink := &wireSink{validator: voiceworker.NewValidator("test-epoch", c), events: make(chan voiceworker.Event, 64)}
			f := &fakeFront{frames: []fakeFrame{{wakes: []Wake{{Phrase: "wake", Span: Span{0, 512}}}, activity: Activity{Speaking: true}}}, flush: Activity{Segments: []Span{{0, 4096}}}}
			ff := &flushSignalFrontend{f, make(chan struct{})}
			started, release, closed := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var once sync.Once
			r := &fakeASR{closed: closed, decode: func([]float32) (string, error) {
				once.Do(func() { close(started) })
				<-release
				return "wake selected dictation", nil
			}}
			runtime := fakeRuntime(f, r, func(string, string) (PCMSource, error) { return pcm, nil })
			runtime.Open = func(NativeConfig) (Frontend, Recognizer, error) { return ff, r, nil }
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- Serve(ctx, in, sink, runtime) }()
			sendCommand(t, parent, "init", &c)
			waitWire(t, sink, "ready")
			sendCommand(t, parent, "start", nil)
			for i := 0; i < 8; i++ {
				if _, err := feedPCM.Write(make([]byte, 1024)); err != nil {
					t.Fatal(err)
				}
				waitWire(t, sink, "level")
			}
			sendCommand(t, parent, "stop", nil)
			<-ff.flushed
			<-started
			switch invalid {
			case "cancel":
				cancel()
			case "partial":
				_, _ = parent.Write([]byte(`{"version":1,"type":"stop"`))
			default:
				sendCommand(t, parent, invalid, nil)
			}
			_ = parent.Close()
			close(release)
			if err := <-done; !errors.Is(err, context.Canceled) {
				t.Fatalf("revoked drain returned %v", err)
			}
			select {
			case <-closed:
			default:
				t.Fatal("ASR owner not joined")
			}
			if !f.closed {
				t.Fatal("frontend not closed")
			}
			for {
				select {
				case e := <-sink.events:
					if e.Type == "note_candidate" || e.Type == "stopped" {
						t.Fatalf("publication after revocation: %+v", e)
					}
				default:
					return
				}
			}
		})
	}
}
