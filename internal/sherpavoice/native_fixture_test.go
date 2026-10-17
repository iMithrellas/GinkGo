//go:build sherpa_onnx && cgo

package sherpavoice

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"math"
	"os"
	"testing"
)

// Private, explicitly opted-in fixture contract; no audio/model/transcript is
// checked into Git. One fixed feed512 replay per case, never a tuning matrix.
type nativeFixture struct {
	Config NativeConfig
	Cases  []struct {
		WAV, SHA256   string
		KeywordFile   string
		ExpectedNotes []struct {
			Start, End int64
			TextSHA256 string
		}
		Events []struct {
			Frames       [7]int64
			Notification int64
		}
	}
}

func fixtureConfig(t *testing.T) nativeFixture {
	t.Helper()
	path := os.Getenv("GINKGO_NATIVE_FIXTURE_CONFIG")
	if path == "" {
		t.Skip("opt-in private native fixture configuration not provided")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var f nativeFixture
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

func fixturePCM(t *testing.T, path, wantHash string) []float32 {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(b)
	if hex.EncodeToString(h[:]) != wantHash {
		t.Fatal("fixture hash mismatch")
	}
	if len(b) < 12 || string(b[:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		t.Fatal("invalid WAV")
	}
	valid := false
	for offset := 12; offset+8 <= len(b); {
		n := int(binary.LittleEndian.Uint32(b[offset+4:]))
		start := offset + 8
		if n > len(b)-start {
			t.Fatal("invalid chunk")
		}
		switch string(b[offset : offset+4]) {
		case "fmt ":
			if n < 16 {
				t.Fatal("invalid format")
			}
			v := b[start : start+n]
			valid = binary.LittleEndian.Uint16(v) == 1 && binary.LittleEndian.Uint16(v[2:]) == 1 && binary.LittleEndian.Uint32(v[4:]) == 16000 && binary.LittleEndian.Uint16(v[14:]) == 16
		case "data":
			if !valid || n%2 != 0 {
				t.Fatal("requires mono16k PCM16")
			}
			p := make([]float32, n/2)
			for i := range p {
				p[i] = float32(int16(binary.LittleEndian.Uint16(b[start+i*2:]))) / 32768
			}
			return p
		}
		offset = start + n + n%2
	}
	t.Fatal("missing PCM")
	return nil
}

func TestNativeKWSPrivateFixtures(t *testing.T) {
	f := fixtureConfig(t)
	for _, c := range f.Cases {
		p := fixturePCM(t, c.WAV, c.SHA256)
		config := f.Config
		if c.KeywordFile != "" {
			config.KeywordFile = c.KeywordFile
		}
		k, err := openNativeKWS(config)
		if err != nil {
			t.Fatal(err)
		}
		found := 0
		for i := 0; i < len(p); i += 512 {
			end := min(i+512, len(p))
			events, err := k.events(p[i:end], false)
			if err != nil {
				k.close()
				t.Fatal(err)
			}
			for _, e := range events {
				if found >= len(c.Events) {
					t.Fatal("unexpected event")
				}
				want := c.Events[found]
				for j, v := range want.Frames {
					if int64(e.frames[j]) != v {
						t.Fatalf("event%d phone%d clock", found, j)
					}
				}
				if int64(e.notification_sample) != want.Notification {
					t.Fatal("notification clock mismatch")
				}
				found++
			}
		}
		tail, err := k.events(nil, true)
		if err != nil {
			t.Fatal(err)
		}
		if len(tail) != 0 {
			t.Fatal("unexpected EOF event")
		}
		k.close()
		k.close()
		if found != len(c.Events) {
			t.Fatalf("got%d want%d", found, len(c.Events))
		}
	}
}

func TestNativeKWSInvalidInputPoisonsStream(t *testing.T) {
	f := fixtureConfig(t)
	for _, p := range [][]float32{{float32(math.NaN())}, {2}, make([]float32, 16001)} {
		k, err := openNativeKWS(f.Config)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := k.events(p, false); err == nil {
			t.Fatal("invalid input accepted")
		}
		if _, err := k.events([]float32{0}, false); err == nil {
			t.Fatal("failed stream reused")
		}
		k.close()
	}
}

func TestNativeCaptionPrivateFixture(t *testing.T) {
	f := fixtureConfig(t)
	if f.Config.RecordingEnabled {
		t.Fatal("fixture must not enable recording")
	}
	front, asr, err := OpenNative(f.Config)
	if err != nil {
		t.Fatal(err)
	}
	defer front.Close()
	defer asr.Close()
	if len(f.Cases) == 0 {
		t.Fatal("missing PCM")
	}
	c := f.Cases[len(f.Cases)-1]
	p := fixturePCM(t, c.WAV, c.SHA256)
	var segment *Span
	wakes := 0
	for i := 0; i < len(p); i += 512 {
		w, a, err := front.Accept(p[i:min(i+512, len(p))])
		if err != nil {
			t.Fatal(err)
		}
		wakes += len(w)
		for _, wake := range w {
			if wake.SpokenForm != "Hey Ginkgo" || wake.Phrase != "NOTE_ALIAS" {
				t.Fatal("English mapping conflated with selection label")
			}
		}
		for _, s := range a.Segments {
			if segment == nil && s.Start >= 0 && s.End <= int64(len(p)) && s.End > s.Start {
				copy := s
				segment = &copy
			}
		}
	}
	if _, err := front.Flush(); err != nil {
		t.Fatal(err)
	}
	if wakes != len(c.Events) || segment == nil {
		t.Fatal("missing real wake/VAD evidence")
	}
	text, err := asr.Decode(p[segment.Start:segment.End])
	if err != nil || text == "" {
		t.Fatalf("real ASR failed: %v", err)
	}
	// Do not print or persist the private transcript in generic test output.
	t.Log("real KWS, Silero and Parakeet initialized; real PCM produced VAD, wakes and nonempty ASR; no notes")
}
