// Package sherpavoice implements the isolated, optional native voice helper.
// It has no storage, network, or public daemon IPC responsibilities.
package sherpavoice

import (
	"errors"
	"fmt"
)

// SampleRate is the only supported PCM clock (mono, normalized float32).
const SampleRate = 16000

// ErrUnsupported identifies a build without the optional native backend.
var ErrUnsupported = errors.New("sherpa-onnx backend not built; requires sherpa_onnx and cgo")

// ErrUnsafeKWSClock prevents recording with the pinned API's unobservable resets.
var ErrUnsafeKWSClock = errors.New("native keyword sample clock unsupported")

func nativeRecordingSupport(enabled bool) error {
	if enabled {
		return ErrUnsafeKWSClock
	}
	return nil
}

// Span is a half-open interval on the capture sample clock.
type Span struct{ Start, End int64 }

// Wake contains acoustic model evidence, not ASR text matching. End is the
// detection boundary, not a claim of precise word-end alignment.
type Wake struct {
	Phrase string
	// SpokenForm is independently verified English speech, never an @alias.
	// Empty means unknown and cannot authorize a selected capture. A backend
	// must derive it from audited tokenizer semantics or a validated mapping.
	SpokenForm string
	Span
}

// Activity contains current speech state and completed Silero segments.
type Activity struct {
	Speaking bool
	Segments []Span
}

// Frontend owns independent CPU keyword and VAD instances. Only the capture
// goroutine may call it. Implementations must not retain the samples slice.
type Frontend interface {
	Accept([]float32) ([]Wake, Activity, error)
	Flush() (Activity, error)
	Close()
}

// Recognizer is resident and owned exclusively by the ASR worker goroutine.
// Decode is offline inference over a snapshot, not token streaming.
type Recognizer interface {
	Decode([]float32) (string, error)
	Close()
}

// ModelFiles names explicitly provisioned model files; nothing is downloaded.
type ModelFiles struct{ Encoder, Decoder, Joiner, Tokens string }

// NativeConfig describes pinned sherpa-onnx v1.13.8 and Parakeet TDT 0.6B v2
// FP32 artifacts. KeywordFile must be compiled for the supplied KWS tokenizer.
type NativeConfig struct {
	RecordingEnabled       bool
	ASR, KWS               ModelFiles
	Silero, KeywordFile    string
	Provider               string
	Threads                int
	SilenceMS, MaxSpeechMS int
}

func (c NativeConfig) validate() error {
	if c.Provider != "cpu" && c.Provider != "cuda" {
		return errors.New("invalid provider")
	}
	if c.Threads < 1 || c.Threads > 32 {
		return errors.New("invalid thread count")
	}
	for _, m := range []ModelFiles{c.ASR, c.KWS} {
		if m.Encoder == "" || m.Decoder == "" || m.Joiner == "" || m.Tokens == "" {
			return errors.New("incomplete model configuration")
		}
	}
	if c.Silero == "" || c.KeywordFile == "" {
		return errors.New("incomplete frontend configuration")
	}
	return nil
}

// ProviderStatus never infers actual CUDA use from a successful constructor.
type ProviderStatus struct{ Requested, Actual, FallbackCode string }

// SelectProvider requires independently verified availability from the linked
// ONNX Runtime. Availability alone is not successful CUDA initialization: ready
// must additionally wait for the native recognizer constructor to return.
func SelectProvider(requested string, cudaAvailable bool) (ProviderStatus, error) {
	switch requested {
	case "cpu":
		return ProviderStatus{Requested: requested, Actual: "cpu"}, nil
	case "cuda", "auto":
		if cudaAvailable {
			return ProviderStatus{Requested: requested, Actual: "cuda"}, nil
		}
		return ProviderStatus{Requested: requested, Actual: "cpu", FallbackCode: "cuda_unavailable"}, nil
	default:
		return ProviderStatus{}, fmt.Errorf("invalid provider selection")
	}
}
