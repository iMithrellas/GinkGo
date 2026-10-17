//go:build sherpa_onnx && cgo

package sherpavoice

/*
#cgo LDFLAGS: -lginkgo-kws -lsherpa-onnx-c-api -lonnxruntime
#include <stdlib.h>
#include <string.h>
#include <sherpa-onnx/c-api/c-api.h>
#include <onnxruntime_c_api.h>

// Probe exactly the linked runtime used by sherpa, not a shell utility or a
// second dlopened runtime. A mismatched version is not trustworthy evidence.
static int ginkgo_cuda_available(void) {
 const OrtApiBase *base = OrtGetApiBase();
 if (!base || strcmp(base->GetVersionString(), SherpaOnnxGetOnnxruntimeVersionStr())) return -1;
 const OrtApi *api = base->GetApi(ORT_API_VERSION);
 if (!api) return -1;
 char **providers = NULL; int count = 0; int found = 0;
 OrtStatus *status = api->GetAvailableProviders(&providers, &count);
 if (status) { api->ReleaseStatus(status); return -1; }
 for (int i=0; i<count; ++i) if (!strcmp(providers[i], "CUDAExecutionProvider")) found=1;
 status = api->ReleaseAvailableProviders(providers, count);
 if (status) { api->ReleaseStatus(status); return -1; }
 return found;
}
static const SherpaOnnxVoiceActivityDetector *ginkgo_vad(const char *model,int threads,float silence,float max_speech) {
 SherpaOnnxVadModelConfig c = {0};
 c.silero_vad.model=model; c.silero_vad.threshold=0.5f; c.silero_vad.min_silence_duration=silence;
 c.silero_vad.min_speech_duration=0.25f; c.silero_vad.max_speech_duration=max_speech; c.silero_vad.window_size=512;
 c.sample_rate=16000; c.num_threads=threads; c.provider="cpu";
 return SherpaOnnxCreateVoiceActivityDetector(&c,max_speech+silence+2.0f);
}
static const SherpaOnnxOfflineRecognizer *ginkgo_asr(const char *e,const char *d,const char *j,const char *t,const char *provider,int threads) {
 SherpaOnnxOfflineRecognizerConfig c = {0};
 c.feat_config.sample_rate=16000; c.feat_config.feature_dim=80;
 c.model_config.transducer.encoder=e; c.model_config.transducer.decoder=d; c.model_config.transducer.joiner=j;
 c.model_config.tokens=t; c.model_config.provider=provider; c.model_config.num_threads=threads;
 c.model_config.model_type="nemo_transducer"; c.decoding_method="greedy_search";
 return SherpaOnnxCreateOfflineRecognizer(&c);
}
*/
import "C"

import (
	"errors"
	"math"
	"strings"
	"sync"
	"unsafe"
)

type nativeFrontend struct {
	mu     sync.Mutex
	vad    *C.SherpaOnnxVoiceActivityDetector
	kws    *nativeKWS
	failed bool
}
type nativeRecognizer struct {
	recognizer *C.SherpaOnnxOfflineRecognizer
}

// ProbeProvider verifies the runtime identity and CUDA EP availability. Pinned
// sherpa session.cc appends CUDA when available; constructor failure must cause
// a fresh CPU child retry before capture (a native exception may abort).
func ProbeProvider(requested string) (ProviderStatus, error) {
	if strings.TrimPrefix(C.GoString(C.SherpaOnnxGetVersionStr()), "v") != "1.13.8" {
		return ProviderStatus{}, errors.New("native version mismatch")
	}
	available := int(C.ginkgo_cuda_available())
	if available < 0 {
		return ProviderStatus{}, errors.New("native runtime verification failed")
	}
	return SelectProvider(requested, available == 1)
}

// OpenNative creates independent frontend and resident offline ASR instances.
// Call only in the isolated helper process; malformed native models may abort.
func OpenNative(c NativeConfig) (Frontend, Recognizer, error) {
	if err := nativeRecordingSupport(c.RecordingEnabled); err != nil {
		return nil, nil, err
	}
	if err := c.validate(); err != nil {
		return nil, nil, err
	}
	s, err := ProbeProvider(c.Provider)
	if err != nil {
		return nil, nil, err
	}
	if s.Actual != c.Provider {
		return nil, nil, errors.New("provider unavailable")
	}
	var allocs []*C.char
	str := func(s string) *C.char { p := C.CString(s); allocs = append(allocs, p); return p }
	defer func() {
		for _, p := range allocs {
			C.free(unsafe.Pointer(p))
		}
	}()
	f := &nativeFrontend{}
	f.kws, err = openNativeKWS(c)
	if err != nil {
		return nil, nil, err
	}
	silence, maxSpeech := c.SilenceMS, c.MaxSpeechMS
	if silence == 0 {
		silence = 500
	}
	if maxSpeech == 0 {
		maxSpeech = 25000
	}
	f.vad = C.ginkgo_vad(str(c.Silero), C.int(c.Threads), C.float(float32(silence)/1000), C.float(float32(maxSpeech)/1000))
	if f.vad == nil {
		f.Close()
		return nil, nil, errors.New("frontend initialization failed")
	}
	r := C.ginkgo_asr(str(c.ASR.Encoder), str(c.ASR.Decoder), str(c.ASR.Joiner), str(c.ASR.Tokens), str(c.Provider), C.int(c.Threads))
	if r == nil {
		f.Close()
		return nil, nil, errors.New("recognizer initialization failed")
	}
	return f, &nativeRecognizer{r}, nil
}
func (f *nativeFrontend) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.kws.close()
	if f.vad != nil {
		C.SherpaOnnxDestroyVoiceActivityDetector(f.vad)
		f.vad = nil
	}
}
func (f *nativeFrontend) activity() (Activity, error) {
	a := Activity{Speaking: C.SherpaOnnxVoiceActivityDetectorDetected(f.vad) != 0}
	for C.SherpaOnnxVoiceActivityDetectorEmpty(f.vad) == 0 {
		s := C.SherpaOnnxVoiceActivityDetectorFront(f.vad)
		if s == nil {
			return a, errors.New("invalid vad segment")
		}
		a.Segments = append(a.Segments, Span{int64(s.start), int64(s.start) + int64(s.n)})
		C.SherpaOnnxDestroySpeechSegment(s)
		C.SherpaOnnxVoiceActivityDetectorPop(f.vad)
	}
	return a, nil
}
func (f *nativeFrontend) Accept(p []float32) ([]Wake, Activity, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failed || f.vad == nil {
		return nil, Activity{}, errors.New("native frontend unusable")
	}
	if len(p) == 0 {
		return nil, Activity{}, nil
	}
	if len(p) > 16000 {
		f.failed = true
		return nil, Activity{}, errors.New("native PCM input too large")
	}
	for _, s := range p {
		if math.IsNaN(float64(s)) || math.IsInf(float64(s), 0) || s < -1 || s > 1 {
			f.failed = true
			return nil, Activity{}, errors.New("invalid native PCM")
		}
	}
	wakes, err := f.kws.accept(p)
	if err != nil {
		f.failed = true
		return nil, Activity{}, err
	}
	// The API consumes/copies samples synchronously and does not retain Go memory.
	C.SherpaOnnxVoiceActivityDetectorAcceptWaveform(f.vad, (*C.float)(unsafe.Pointer(&p[0])), C.int32_t(len(p)))
	a, err := f.activity()
	if err != nil {
		f.failed = true
		return nil, Activity{}, err
	}
	return wakes, a, nil
}
func (f *nativeFrontend) Flush() (Activity, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failed || f.vad == nil {
		return Activity{}, errors.New("native frontend unusable")
	}
	C.SherpaOnnxVoiceActivityDetectorFlush(f.vad)
	a, err := f.activity()
	if err != nil {
		f.failed = true
	}
	return a, err
}
func (r *nativeRecognizer) Close() {
	if r.recognizer != nil {
		C.SherpaOnnxDestroyOfflineRecognizer(r.recognizer)
		r.recognizer = nil
	}
}
func (r *nativeRecognizer) Decode(p []float32) (string, error) {
	if len(p) == 0 {
		return "", nil
	}
	s := C.SherpaOnnxCreateOfflineStream(r.recognizer)
	if s == nil {
		return "", errors.New("recognizer stream failed")
	}
	defer C.SherpaOnnxDestroyOfflineStream(s)
	C.SherpaOnnxAcceptWaveformOffline(s, SampleRate, (*C.float)(unsafe.Pointer(&p[0])), C.int32_t(len(p)))
	C.SherpaOnnxDecodeOfflineStream(r.recognizer, s)
	result := C.SherpaOnnxGetOfflineStreamResult(s)
	if result == nil {
		return "", errors.New("recognizer result failed")
	}
	defer C.SherpaOnnxDestroyOfflineRecognizerResult(result)
	return C.GoString(result.text), nil
}
