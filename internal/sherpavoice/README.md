# Optional native ginkgo-voice helper

**BLOCKED: native note recording is unavailable.** Pinned v1.13.8 resets KWS
decoder frame offsets both explicitly and automatically after 1.5 seconds of
trailing blanks, while retaining online-stream duplicate timestamps and its old
time origin. The public C API exposes no reset generation or new clock origin.
The previous `start_time + timestamps[0]` attribution was unsafe. Native init now
rejects `RecordingEnabled=true` before constructing engines or opening capture,
with `ErrUnsafeKWSClock`. CPU fallback cannot fix it. Caption-only native init
remains implemented; fake-engine selected-note tests are **not native support**.
Restoring recording requires an audited upstream fix/clock shim covering BOTH
reset paths and duplicate suppression. Short replacement streams are not used:
they could lose long phrases or same-breath speech. No timestamps are fabricated.

`cmd/ginkgo-voice` is a separate optional process. Neither the normal CLI nor
`ginkgod` imports native code. Ordinary builds include an explicit unsupported
backend; a valid init exits nonzero without stdout events and prints a generic
missing-native-build diagnostic. No dependencies or models are fetched at runtime.

## Build and provisioning

An operator must provision **official sherpa-onnx v1.13.8** headers and shared
libraries and the **same ONNX Runtime distribution that sherpa was linked to**.
For CUDA, use a CUDA-enabled ONNX Runtime and its matching CUDA/cuDNN dependencies.
Do not mix a CPU Go-module prebuilt runtime with a different GPU runtime.

Example using already installed, trusted prefix directories (no installation or
download is performed by this command):

```sh
CGO_ENABLED=1 \
CGO_CFLAGS="-I${SHERPA_PREFIX}/include -I${ORT_PREFIX}/include" \
CGO_LDFLAGS="-L${SHERPA_PREFIX}/lib -L${ORT_PREFIX}/lib -Wl,-rpath,${SHERPA_PREFIX}/lib -Wl,-rpath,${ORT_PREFIX}/lib" \
go build -tags sherpa_onnx -o build/ginkgo-voice ./cmd/ginkgo-voice
```

Required headers are `sherpa-onnx/c-api/c-api.h` and `onnxruntime_c_api.h`
(including its distribution's associated headers). Link libraries are
`libsherpa-onnx-c-api` and `libonnxruntime`. Avoid writable/untrusted library
search directories. This package adds no Go dependency.

Provision English **Parakeet TDT 0.6B v2 FP32**, specifically the upstream
`sherpa-onnx-nemo-parakeet-tdt-0.6b-v2.tar.bz2` export's `encoder.onnx`,
`decoder.onnx`, `joiner.onnx`, and `tokens.txt`. Configure
`model_type=nemo_transducer` (the wrapper does so explicitly). Do not substitute
Parakeet unified, v3, or an assumed CUDA/CPU-compatible int8/fp16 export.
The helper trusts provisioning; it does not prove model identity from arbitrary
file paths or hashes.

Also provision a compatible English streaming transducer acoustic KWS graph,
its tokenizer/tokens, a **matching precompiled keyword file**, and Silero VAD's
ONNX model. A keyword label is not a tokenizer/compiler. Changing a phrase in
configuration cannot magically make it detectable: compile matching keyword
tokens externally first. `SelectedKeyword`, if nonempty, filters actual acoustic
KWS detections; ASR text never activates recording.

Pinned references:

- [KWS manual/automatic resets and timestamp conversion](https://github.com/k2-fsa/sherpa-onnx/blob/v1.13.8/sherpa-onnx/csrc/keyword-spotter-transducer-impl.h)
- [Retained KWS duplicate timestamps](https://github.com/k2-fsa/sherpa-onnx/blob/v1.13.8/sherpa-onnx/csrc/online-stream.cc)
- [C API](https://github.com/k2-fsa/sherpa-onnx/blob/v1.13.8/sherpa-onnx/c-api/c-api.h)
- [CUDA provider selection/fallback](https://github.com/k2-fsa/sherpa-onnx/blob/v1.13.8/sherpa-onnx/csrc/session.cc)
- [v2 FP32 export workflow](https://github.com/k2-fsa/sherpa-onnx/blob/v1.13.8/.github/workflows/export-nemo-parakeet-tdt-0.6b-v2.yaml)

## Process API and lifecycle

No model, provider, or microphone flags are exposed. Run `ginkgo-voice` with no
arguments through `voiceworker.Client`; `-h` prints build/transport usage.
Inherited stdin/stdout use **only** the shared strict `voiceworker` v1 NDJSON
contract. See [`../voiceworker/README.md`](../voiceworker/README.md) for all fields.

`Serve(ctx, inputReadCloser, outputWriter, Runtime)` is the injectable entry point.
It owns and closes input; Close must unblock Read. `Runtime` injects provider
probing, model construction, and recorder construction. The native runtime uses
the trusted absolute recorder executable/optional target from validated init.
Only newline-completed command frames are decoded. An unterminated final JSON
object is an abort, including init, start, or stop; it is never an implicit stop.

Lifecycle is `init -> ready -> start -> events -> stop -> stopped`. Model
initialization and provider selection finish **before ready**; only explicit
`start` opens one PCM recorder. Stop instead of start never opens the recorder.
Init failure emits no protocol events, permitting the supervisor's single fresh
CPU attempt with a new epoch. A retry honors `Config.ExecutionProvider()` and
preserves `cuda_init_failed` while reporting the original requested policy.
There is no retry after ready. Stop flushes and drains eligible selected speech;
EOF, repeated/invalid commands, cancellation, and capture faults discard work.
After the one validated stop, the control reader remains authoritative until it
observes the expected EOF. Flush/native decoding may begin while awaiting EOF,
but drain results cannot be published before that barrier. A second stop, start
after stop, partial frame, or other command revokes the capture context. Context
cancellation is rechecked at result publication and by the bounded output writer;
revoked drains cannot emit candidates or successful stopped. Native owners are
joined after calls return, never destroyed concurrently with inference. If a
native call does not return, the supervising parent must kill its process group.

Capture is exactly 16 kHz mono signed little-endian PCM16 from one `pw-record`
process, using fixed `--rate`, `--channels`, `--format=s16`, and `--raw` options.
There is no shell, HTTP, storage, AI postprocessing, or network. Close kills and
waits for the recorder, and both helper and recorder remain in the supervisor's
process group. A five-second missing-PCM watchdog detects recorder stalls;
ordinary silent audio still has frames and is not a stall.

## Native/provider ownership

Silero runs on CPU in the capture owner. Native KWS construction and calls have
been removed while its sample clock is unsafe; enabling recording fails closed.
A separate single ASR goroutine exclusively owns the resident offline recognizer
and its destructor. Constructors copy native configuration before temporary C
strings are freed. Each decode owns/destroys a fresh offline stream and result;
Copied VAD segments are destroyed after consumption. Sample
buffers are passed only to synchronous waveform acceptance calls; no Go sample
pointer is retained by C. The frontend is closed only after capture stops.

The wrapper verifies sherpa version `1.13.8`, checks the linked ORT version
against sherpa's reported ORT version, and uses that linked runtime's C API
`GetAvailableProviders`. Under pinned `session.cc`, CUDA availability causes
`AppendExecutionProvider_CUDA`; ready follows successful recognizer construction.
Absent CUDA can explicitly negotiate CPU with `cuda_unavailable` only when
fallback is allowed. CUDA initialization aborts/exceptions are handled by a fresh
CPU helper attempt **before capture**, not by restarting a recorder in-process.
`effective=cuda` means CUDA EP initialization, **not that all graph nodes, KWS,
VAD, decoder, or joiner execute on the GPU**. No inference/profiling is performed
just to probe availability. The process boundary is not an OS sandbox, and
uninterruptible native calls require the parent's process-group kill deadline.

## Audio, captions, and selected-note provenance

All boundaries are half-open sample-clock offsets, not wall time. The bounded
ring retains source audio without padding missing samples. Caption IDs/revisions
are independent of selected-note session/capture IDs. Continuous ambient captions
remain available with note recording disabled. Only `note_candidate` is a
selected finalized result; caption attribution can never authorize saving.

For injectable, trusted frontends, a verified epoch-relative acoustic span fixes
the selected start. The disabled native KWS backend cannot currently supply that
guarantee. The wake end is its **detection extent**, not a phrase-end claim.
The selection excludes the preceding ambient utterance. Phrase-alone wake plus
pause and subsequent dictation retains the exact original selected start and ID;
it is not replaced with the next ambient utterance's audio. Only qualifying
Silero speech/endpoints select content; short discarded pulses do not strand the
gate. Trigger-only/empty decoding produces no candidate. Cooldown and audio-time
timeout bound pending selection, and no second wake can borrow an unfinished
selected session's attribution. Max-recording duration includes the wake and
pause, not just dictation.

**Conservative protocol v1 limitation:** KWS does not provide a safe phrase-end
audio boundary. The helper therefore emits exact `Original` and `Body=Original`,
without an invented `control` span. The leading phrase can remain in the note.
Internally a separately verified `Wake.SpokenForm` is recognized only to reject
trigger-only content; this is never used to authorize wake. Precise body-prefix
removal needs proven alignment or a separately reviewed protocol extension.
`Wake.Phrase` is a selection label and may be an `@alias`; it is never assumed to
be spoken text. Unknown canonical forms cannot open selected captures. A future
native implementation needs audited English tokenizer decoding or a validated
explicit spoken-form mapping in addition to the clock fix. The current native
implementation supplies neither and therefore emits no wake events.

ASR previews are repeated **offline** snapshots, not token streaming. Defaults
are 500 ms cadence over a five-second tail. A single pending preview is coalesced;
finals have a separate eight-job bounded FIFO and cannot be silently dropped.
Submitted finals suppress stale previews. The eight-frame capture queue,
bounded audio ring, ASR result queue, and eight-line stdout queue fault on
overflow rather than blocking capture or growing unboundedly. Stdout draining
has a one-second deadline; process exit/parent kill ends a blocked OS writer.
Level events are throttled at the configured update cadence. Protocol v1 has no
VAD/speech boolean or heartbeat payload; the engine's native speaking state is
used for preview/endpoint decisions, not represented as a fabricated wire field.

The helper supports `WindowMS <= 30000`, `MaxRecordingMS >= 250`, and total ring
retention `MaxRecordingMS + max(PreRollMS, WindowMS) + SilenceMS + 1000 <= 120000`.
Unsupported durations fail before model initialization, never silently truncate
requested settings. Preview cadence is naturally quantized to 512-sample capture
frames (32 ms). Shared validation additionally caps text at 16 KiB and session /
utterance history at 4096 per epoch; rotate long-lived captures before that cap.

## Verification and remaining limits

```sh
CGO_ENABLED=0 go test ./...
go test -race ./internal/sherpavoice ./internal/voiceworker
```

Fake tests cover acoustic-versus-text wake, captions-only opt-out, Silero endpoint
selection, same-breath boundaries, phrase-alone/pause retention, discarded noise,
cooldown/timeout, duration caps, source-clock ring wrap, revisions/partials before
silence, slow-ASR isolation/final overflow, bounded stdout, init/provider fallback,
handshake authorization, stop/cancel/EOF, and fake recorder termination/reaping.
Safety regressions also cover alias versus canonical spoken form, unknown-form
rejection, both pinned reset/duplicate behaviors (a source model, not native
execution), the native recording guard before C calls, every unterminated command
type, and cancellation/duplicate stop/start/partial commands during blocked ASR
draining. The independent four-runtime-reproduction overlay passes under repeated
race runs. These tests do not restore or prove a corrected native KWS clock.

Implementation verification used fetched **text headers only** (sherpa v1.13.8
and ORT v1.23.2 plus its EP header) and locally generated no-op link shims.
`go tool cgo` and tagged Go/race tests compiled successfully against those
declarations. Those tests exercise fake engines, **not real native constructors,
model quality, CPU inference, CUDA, microphone capture, or driver compatibility**.
An ordinary tagged native build in this environment fails explicitly at the
missing installed `sherpa-onnx/c-api/c-api.h`. No models/native libraries were
downloaded, installed, or executed. Real native integration remains unverified.
