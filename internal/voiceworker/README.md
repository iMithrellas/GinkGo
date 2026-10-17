# Private ginkgo-voice protocol v1

This pure-Go package has no native dependencies, audio access, note storage,
network, or public IPC. `New(ClientConfig{Executable: absoluteInstalledHelper})`
is lazy. `Run(ctx, Config, stop <-chan struct{}, emit func(Event) error)` owns
one capture lifetime, not any public-event subscriber's lifetime. Disabled runs
return without validation, random generation, filesystem I/O, or launching.
Integration must keep public `internal/voiceevents` unchanged.

## Transport and handshake

Inherited stdin/stdout carry UTF-8 NDJSON, one object per newline (maximum 65536
bytes excluding newline). stdout is **only protocol**, stderr is discarded and
limited to 64 KiB per process; it is never included in errors. No shell is used
to launch the installed helper. The helper and recorder must stay in the
supervisor-owned process group: do not daemonize, setsid, or change process group.

1. Parent sends `Command{Version:1, Type:"init", Epoch:<random>, Config:&cfg}`.
2. Child initializes all models **without opening capture**, then sends `ready`.
3. Parent validates ready and sends `Command{Version:1, Type:"start", Epoch:...}`.
   Only receipt of start authorizes capture. Ready is not itself authorization.
4. Closing stop sends `Command{Version:1, Type:"stop", Epoch:...}`, then stdin
   EOF. Stop can arrive instead of start, even during model initialization.
   Gracefully finalize/drain any selected note (or emit note_canceled), emit
   stopped, close output, and exit zero. A stop before start must not open mic.
5. Unexpected EOF, cancellation, errors, or crashes abort without saving partials.
   A normal start keeps stdin open until stop; an unexpected EOF is not graceful.

`DecodeCommand` is shared strict decoding; the helper must enforce init exactly
once, matching epoch, start at most once after ready, and stop at most once.
All required struct fields (JSON tags without omitempty) must be present,
including false booleans, zero span starts, and empty fallback_reason. No null,
unknown/case-aliased fields, duplicate keys, arrays, or trailing JSON are allowed.

## Config and CUDA retry

`Config` and `Graph` are the source of truth for wire names. Trusted absolute
installed paths are passed privately: recorder executable/target, separate
Parakeet and KWS encoder/decoder/joiner/tokens, **compiled** keywords file, and
Silero model. No assumption is made about phrase-to-token compilation APIs.
Audio is fixed 16kHz mono signed little-endian int16 PCM; spans use audio samples
since the process epoch. Durations use integer `_ms`: positive update/window/
silence/max_recording/trigger_timeout <=1h, cooldown 0..60s, pre_roll 0..30s.
Window >= update and silence <= max_recording. Recording is explicitly opt-in;
SelectedKeyword is an optional private label. No paths belong in diagnostics.

`Provider` is the original requested cpu/cuda/auto policy. Auto prefers CUDA;
CPU fallback requires AllowCPUFallback. `ExecutionProvider()` selects this
process's attempt. On the initial attempt AttemptProvider and FallbackReason
are absent. If a process/transport failure or ready timeout occurs **before any
validated ready**, the supervisor may retry once with AttemptProvider=cpu and
FallbackReason=cuda_init_failed. It preserves Provider, creates a fresh epoch,
and never retries after ready, stop, cancellation, or a failed CPU retry.
Malformed protocol is not retryable. This handles native aborts before ready.
Helper must honor a CPU retry and report effective CPU with that fallback reason.
Initialization failure must exit nonzero without stdout events; `error` is only
valid after ready. This preserves the first-ready rule and permits safe retry.

Ready carries `ProviderStatus{Requested, Effective, FallbackReason}`. Effective
may be unknown; CPU under auto/cuda requires explicit allowed fallback and a
bounded reason: cuda_unavailable or cuda_failed (initial attempt), or
cuda_init_failed (supervisor retry). Never include native errors/paths.
Query the **same linked ORT** available providers before claiming CUDA (pinned
sherpa can silently omit unavailable CUDA). Successful CUDA EP initialization
under pinned source is not a claim that every graph node runs on GPU.

## Events

Every `Event` has required version=1, type, epoch, sequence (starts 1, consecutive),
and recording_note (non-null bool). The first event must be ready. Each type
permits only its listed payload; use the exported Go structs to encode it.

| type | payload | semantics |
|---|---|---|
| ready | provider | once, recording_note=false, no capture yet |
| state | optional session_id | immediate audio-time recording transition |
| level | level | finite normalized 0..1 |
| wake | session_id, utterance_id, audio | new unique selected session; recording_note=true |
| transcript | transcript | ambient revision or final; **never save authorization** |
| note_candidate | candidate | sole finalized selected-note candidate; recording_note=false |
| note_canceled | session_id, code | timeout/empty/control_only/user/shutdown; recording_note=false |
| error | code | terminal capture_failed/model_failed/inference_failed/provider_unavailable/internal |
| stopped | none | terminal, no pending selected session, recording_note=false |

Wake must be driven by the worker audio-time KWS gate independently of ASR
finalization. Its utterance_id identifies the **selected note capture**, not an
ambient-caption utterance. Selected-capture IDs must be unique for the epoch and
must never be used for ambient transcripts (in either ordering). Wake audio.Start
freezes the selected capture's start sample, including intentional keyword and
pre-roll audio; wake audio.End is the extent already captured at acoustic wake.
The worker must retain that selected audio through finalization. One selected
capture may include the wake phrase, ensuing silence, and subsequent dictation.
Ambient captions may remain continuous while recording_note=false.
State may turn recording off while selected ASR finalization is pending; do not
start another selected session until the prior candidate/cancellation is emitted.
Level/transcript/error must reflect the current recording state.

`Transcript` has utterance_id, monotonically increasing revision >=1, final,
text, audio, metadata. Nothing follows a final for that utterance. `Candidate`
has unique session_id from wake, utterance_id, original, body, audio, metadata,
and optional control/control_text. Original is **exact selected-audio transcript**.
Candidate session_id and utterance_id must both equal the pending wake's IDs.
Candidate audio.Start must equal wake audio.Start and candidate audio.End must
be >= wake audio.End. Both wake and candidate spans must be positive and total
selected-capture length must be <= MaxRecordingMS * 16 samples, **including any
keyword, pre-roll, silence, and subsequent dictation**. The multiplication is
checked against bounded configuration before use. Earlier, disjoint, shifted,
shortened, or oversized candidate audio is rejected. An inactive state while
finalizing retains these pending IDs and audio bounds until candidate/cancellation.
Without safely identified control prefix, Body must equal Original. If excluded,
Control must be a prefix sample span within Audio, ControlText must be an exact
Original prefix, and Body must equal TrimSpace(Original minus ControlText).
Do not guess removal; omit control fields when uncertain. Supervisor validates
structure, not acoustic truth. Each session may yield only one candidate or
cancellation; replay is rejected. Text is capped at 16KiB; nonempty body is
required. Metadata uses engine=sherpa-onnx, model=parakeet-tdt-0.6b-v2, language=en.
At most 4096 utterances and 4096 selected sessions per epoch bound validation
memory; integrator should rotate long-lived captures before this limit.

## Parent integration and limits

Only finalized validated selected candidates may be persisted, with opt-in and
**parent-rebound trusted engine/model/language metadata**; no ambient event,
revision, error, or partial is authorization. This package does not persist or
publish. There is no native probing in constructors or disabled calls.

Startup defaults 30s, graceful shutdown 5s (configurable up to 5m).
On observing stop, the startup deadline is retired, even before ready; the
shutdown budget alone governs finalization. If stop and startup expiry are both
observable together, stop takes precedence. No start or fallback is issued after
stop is observed.
Silence has
no idle-stall timeout: a live process can be silent indefinitely. Cancellation
closes pipes and SIGKILLs the entire process group; every direct child is waited.
Normal exits also kill remaining group descendants before returning. Escaped
process groups are outside this contract; stronger hostile-executable isolation
requires an OS cgroup/sandbox. Descendant zombies are reaped by their OS parent,
not this package (which does not alter daemon-global subreaper state).

The output queue holds at most eight bounded lines plus one callback delivery;
backpressure never disables stop/deadline/cancellation handling. `emit` is
serialized, should return promptly, and its errors are preserved. One stuck
caller callback cannot block process cleanup, but Go cannot forcibly cancel
arbitrary caller code: it can retain one callback goroutine until it returns.
No idle callback deadline is imposed. Native stderr is never returned to callers.
