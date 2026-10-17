//go:build sherpa_onnx && cgo

package sherpavoice

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

type recognitionEvidence struct {
	Samples      int
	SHA256, Text string
}
type observedRecognizer struct {
	Recognizer
	calls []recognitionEvidence
}

func pcmHash(p []float32) string {
	b := make([]byte, len(p)*4)
	for i, v := range p {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(v))
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func (r *observedRecognizer) Decode(p []float32) (string, error) {
	text, err := r.Recognizer.Decode(p)
	r.calls = append(r.calls, recognitionEvidence{len(p), pcmHash(p), text})
	return text, err
}

func TestNativeSelectedPrivateFixtures(t *testing.T) {
	f := fixtureConfig(t)
	out := os.Getenv("GINKGO_NATIVE_SELECTED_EVIDENCE")
	if out == "" {
		t.Skip("private selected evidence directory not supplied")
	}
	if err := os.MkdirAll(out, 0700); err != nil {
		t.Fatal(err)
	}
	for index, c := range f.Cases {
		p := fixturePCM(t, c.WAV, c.SHA256)
		config := f.Config
		config.RecordingEnabled = true
		front, r, err := OpenNative(config)
		if err != nil {
			t.Fatal(err)
		}
		observed := &observedRecognizer{Recognizer: r}
		var events []Event
		ec := DefaultEngineConfig()
		ec.PreviewEvery = 16000
		ec.FinalQueue = 8
		e, err := NewEngine(ec, front, observed, func(v Event) error { events = append(events, v); return nil })
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(e.Cancel)
		for i := 0; i < len(p); i += 512 {
			if err := e.Process(p[i:min(i+512, len(p))]); err != nil {
				t.Fatal(err)
			}
			// File-fed offline test: drain selected work before advancing virtual
			// audio time. Production is asynchronous; deterministic lag/rearm tests
			// separately cover late callbacks. No fake ASR or model results here.
			for e.gate != nil && e.gate.pending > 0 {
				result, ok := <-e.results
				if !ok {
					t.Fatal("ASR ended")
				}
				if err := e.result(result); err != nil {
					t.Fatal(err)
				}
			}
		}
		if err := e.Stop(); err != nil {
			t.Fatal(err)
		}
		// Worker is joined. Every candidate must bind byte-identical source PCM
		// to an actual real-model decode, not a caption copied into a selection.
		notes := 0
		for _, v := range events {
			if v.Type == "note_candidate" {
				if notes >= len(c.ExpectedNotes) {
					t.Fatal("unexpected selected note")
				}
				want := c.ExpectedNotes[notes]
				notes++
				h := sha256.Sum256([]byte(v.Original))
				if v.Start != want.Start || v.End != want.End || hex.EncodeToString(h[:]) != want.TextSHA256 {
					t.Fatal("selected command span/text differs from reviewed whole-command expectation")
				}
				if v.Start < 0 || v.End > int64(len(p)) {
					t.Fatal("candidate source bounds")
				}
				hash := pcmHash(p[v.Start:v.End])
				matched := false
				for _, call := range observed.calls {
					if call.SHA256 == hash && call.Text == v.Original {
						matched = true
					}
				}
				if !matched {
					t.Fatal("selected PCM/ASR binding failed")
				}
			}
		}
		if notes != len(c.ExpectedNotes) {
			t.Fatalf("candidates got%d want%d", notes, len(c.ExpectedNotes))
		}
		evidence := struct {
			Events  []Event
			Decodes []recognitionEvidence
		}{events, observed.calls}
		b, err := json.MarshalIndent(evidence, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(out, "selected-"+strconv.Itoa(index)+".json"), b, 0600); err != nil {
			t.Fatal(err)
		}
		if count(events, "wake") != len(c.Events) {
			t.Fatalf("accepted wakes got%d want%d", count(events, "wake"), len(c.Events))
		}
		t.Logf("fixture%d: accepted acoustic wakes%d, candidates%d; private PCM/ASR binding checked", index, count(events, "wake"), count(events, "note_candidate"))
	}
}
