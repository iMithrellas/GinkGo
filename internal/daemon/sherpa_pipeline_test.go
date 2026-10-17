package daemon

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mithrel/ginkgo/internal/ipc"
	"github.com/mithrel/ginkgo/internal/ipc/transport"
	"github.com/mithrel/ginkgo/internal/sherpavoice"
	"github.com/mithrel/ginkgo/internal/voiceevents"
)

// These factories substitute only acoustic/native engines and PCM. The real
// Serve, Engine, Capture, protocol validator, supervisor, public IPC and database
// path are exercised end to end, in a separate installed helper process.
type pipelineFrontend struct {
	end      int64
	accepted chan struct{}
}

func (f *pipelineFrontend) Accept(p []float32) ([]sherpavoice.Wake, sherpavoice.Activity, error) {
	f.end += int64(len(p))
	var wakes []sherpavoice.Wake
	if f.end == 512 {
		wakes = []sherpavoice.Wake{{Phrase: "GINKGO", SpokenForm: "ginkgo", Span: sherpavoice.Span{Start: 0, End: 512}}}
	}
	f.accepted <- struct{}{}
	return wakes, sherpavoice.Activity{Speaking: true}, nil
}
func (f *pipelineFrontend) Flush() (sherpavoice.Activity, error) {
	return sherpavoice.Activity{Segments: []sherpavoice.Span{{Start: 0, End: f.end}}}, nil
}
func (f *pipelineFrontend) Close() {}

type pipelineRecognizer struct{}

func (pipelineRecognizer) Decode([]float32) (string, error) { return "GINKGO exact selected note", nil }
func (pipelineRecognizer) Close()                           {}

type pipelinePCM struct {
	*io.PipeReader
	writer       *io.PipeWriter
	cancel, done chan struct{}
	once         sync.Once
}

func (p *pipelinePCM) Close() error {
	p.once.Do(func() { close(p.cancel); _ = p.PipeReader.Close(); _ = p.writer.Close(); <-p.done })
	return nil
}

func runSherpaFakePipeline(logPath string) error {
	log := func(s string) {
		f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		if err == nil {
			fmt.Fprintln(f, s)
			f.Close()
		}
	}
	f := &pipelineFrontend{accepted: make(chan struct{}, 1)}
	runtime := sherpavoice.Runtime{
		Probe: func(p string) (sherpavoice.ProviderStatus, error) {
			log("probe " + p)
			return sherpavoice.SelectProvider(p, true)
		},
		Open: func(c sherpavoice.NativeConfig) (sherpavoice.Frontend, sherpavoice.Recognizer, error) {
			log("open " + c.Provider)
			return f, pipelineRecognizer{}, nil
		},
		Microphone: func(string, string) (sherpavoice.PCMSource, error) {
			log("microphone")
			r, w := io.Pipe()
			p := &pipelinePCM{PipeReader: r, writer: w, cancel: make(chan struct{}), done: make(chan struct{})}
			go func() {
				defer close(p.done)
				frame := make([]byte, 1024)
				for i := 0; i < len(frame); i += 2 {
					binary.LittleEndian.PutUint16(frame[i:], 8192)
				}
				for i := 0; i < 22; i++ {
					if _, err := w.Write(frame); err != nil {
						return
					}
					select {
					case <-f.accepted:
					case <-p.cancel:
						return
					}
				}
				log("frames")
				<-p.cancel
			}()
			return p, nil
		},
	}
	return sherpavoice.Serve(context.Background(), os.Stdin, os.Stdout, runtime)
}

type sherpaIPCListener struct{ net.Listener }

func (l sherpaIPCListener) Listen(context.Context) (net.Listener, error) { return l.Listener, nil }
func sherpaPublicControl(t *testing.T, v *voiceRuntime) func(ipc.Message) ipc.Response {
	t.Helper()
	path := filepath.Join(t.TempDir(), "public.sock")
	listener, err := net.Listen("unix", path)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- transport.NewUnixServer(sherpaIPCListener{listener}).Serve(ctx, ipc.PBHandler(func(m ipc.Message) ipc.Response {
			switch m.Name {
			case "voice.stream":
				return v.stream(context.Background(), m)
			case "voice.status":
				return v.voiceStatus()
			case "voice.stop":
				return v.stopListener(true)
			case "voice.cancel":
				return v.stopListener(false)
			default:
				return ipc.Response{Msg: "unsupported test request"}
			}
		}))
	}()
	t.Cleanup(func() { cancel(); require.NoError(t, <-done) })
	return func(m ipc.Message) ipc.Response {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		r, err := ipc.Request(ctx, path, m)
		require.NoError(t, err)
		return r
	}
}

func TestSherpaFullFakeEnginePublicIPCPipeline(t *testing.T) {
	for _, save := range []bool{false, true} {
		t.Run(fmt.Sprintf("save=%t", save), func(t *testing.T) {
			v, events, path := sherpaRuntime(t, "full-engine")
			request := sherpaPublicControl(t, v)
			r := request(ipc.Message{Name: "voice.stream", Save: save, Trigger: "GINKGO", Namespace: "work"})
			require.True(t, r.OK, r.Msg)
			events.wait(t, func(e voiceevents.Event) bool { return e.Type == "transcript" && !e.Final })
			require.Eventually(t, func() bool { b, _ := os.ReadFile(path); return strings.Contains(string(b), "frames") }, 5*time.Second, time.Millisecond)
			status := request(ipc.Message{Name: "voice.status"}).VoiceStatus
			require.Equal(t, "sherpa", status.Backend)
			require.Equal(t, "cuda", status.EffectiveProvider, "fake provider evidence only")
			if save {
				events.wait(t, func(e voiceevents.Event) bool { return e.RecordingNote })
			}
			r = request(ipc.Message{Name: "voice.stop"})
			require.True(t, r.OK, r.Msg)
			if save {
				saved := events.wait(t, func(e voiceevents.Event) bool { return e.Type == "note.saved" })
				entry, err := v.notes.ScopedGet(context.Background(), saved.NoteID, "work")
				require.NoError(t, err)
				require.Equal(t, "GINKGO exact selected note", entry.Body, "current helper conservatively retains control phrase")
				require.Equal(t, entry.Body, entry.Provenance.Sources[0].OriginalText)
			} else {
				require.Empty(t, r.VoiceStatus.LastNoteID)
			}
			b, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, "probe auto\nopen cuda\nmicrophone\nframes\n", string(b))
		})
	}
}
