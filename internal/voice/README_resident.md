# Resident whisper.cpp adapter (Linux)

`Resident` is independent of capture, trigger gating, note persistence, IPC and
chunk scheduling. It submits complete in-memory WAV requests to **installed
`whisper-server`**, loading one installed GGML model for the session. It does not
replace or change the existing on-demand `Transcriber`/`whisper-cli` API.
The daemon's explicit [`voice stream` integration](../../docs/voice-stream.md)
uses this adapter; legacy commands remain on demand.
There are no new Go dependencies, CGO, downloads or helper executables.

## Integration

```go
r := voice.NewResident(voice.ResidentConfig{
    Config: voice.Config{
        Enabled: true,
        Executable: "/trusted/install/bin/whisper-server",
        Model: "/trusted/models/ggml-base.bin",
        Device: "auto", // or "cpu"; explicit "gpu" is unsupported
    },
    AllowLocalHTTP: true, // only on a machine with trusted local users/processes
})
defer r.Close()
result, err := r.Transcribe(ctx, completePCM16WAV, voice.Options{Language: "en"})
info := r.Info()
```

Public signatures:

```go
func NewResident(ResidentConfig) *Resident
func (*Resident) Transcribe(context.Context, []byte, Options) (Transcript, error)
func (*Resident) Info() ResidentInfo
func (*Resident) Close() error
```

`ResidentConfig` embeds existing `Config`, adding `StartupTimeout` (default one
minute) and `AllowLocalHTTP` (default false). Embedded `Timeout` (default five
minutes) includes queueing, startup and inference. Other existing Config defaults
and hard bounds apply: 32 MiB WAV, five minutes audio, 8 GiB GGML model, 1 MiB
response/log budget. MaxOutputBytes bounds **each HTTP response** and **combined
stdout/stderr bytes for the worker's lifetime**; diagnostics are discarded, never
retained or returned. A long/noisy session can exhaust its log budget and fail;
the next call then starts a new worker. Tune within existing hard limits if needed.

Construction and disabled requests do no path probes or I/O. Enabled calls
require explicit `AllowLocalHTTP` consent before any I/O. A session serializes
requests, supports cancellation while queued/loading/inferencing, and retains
the model until Close or a worker/request failure. A canceled queued request does
not disturb an active request. Active cancellation/failure kills the worker rather
than relying on version-dependent server abort callbacks. Close cancels queued and
active work, kills the process group, waits for reap, and permanently closes the
session. Call it when the recording session ends; there is no idle timeout.

Error precedence: disabled calls always return ErrDisabled. Other calls begun
after shutdown starts return ErrResidentClosed, even if their caller context is
already canceled. For calls begun while open, caller cancellation/deadlines
observed during admission take precedence over concurrent shutdown; shutdown
alone returns ErrResidentClosed during admission. Once admitted for engine work,
Close cancels that work with context.Canceled as before.

The caller owns the WAV slice and must not mutate it during the call. Format is
RIFF/WAVE, signed PCM16, mono, 16 kHz; existing `validateWAV` checks all bounds
before startup. Submit rolling windows for partial revisions, then a complete
utterance for the final result. This is repeated resident inference, **not** token
streaming or an incremental decoder; the caller supplies revision/event identity.
Do not append partial texts blindly, or persist them as final notes automatically.
Options.Language is per-request. A different Options.Model is rejected (make a new
session). Model metadata is its resolved installed path. Text is the server's exact
JSON `text`, including its added segment newlines. Language metadata is the requested
language, or empty for auto; it is not an attested detected-language result.

## Upstream protocol compatibility

Reviewed against upstream `ggml-org/whisper.cpp` commit
[`0c7be696371069e0baa2515ed7aa08d5a5473aa6`](https://github.com/ggml-org/whisper.cpp/blob/0c7be696371069e0baa2515ed7aa08d5a5473aa6/examples/server/server.cpp).
Flags/schema are source-verified and exercised by deterministic fake-process
compatibility tests; **no actual model, GPU or installed server inference was
executed for this change**. Older/different installations may be incompatible.

* Executable is upstream's `whisper-server`, not `whisper-cli` or `whisper-stream`.
  Use a trusted existing installation (including its shared libraries/backends).
* Flags: `--host 127.0.0.1 --port <ephemeral> --request-path <random-prefix>`;
  `--public <non-directory>` disables static-file serving; `-m <pinned-model-fd>`;
  `-t <threads> -l <language> -nt`; CPU adds `--no-gpu`.
* Server initializes the model before binding/listening. Repeated POSTs reuse it.
* POST `<random-prefix>/inference`, `multipart/form-data`, fields `file` (WAV,
  fixed filename `audio.wav`), `response_format=json`, `language=<validated-code>`.
* Expected HTTP 200 with JSON object containing a string `text`. JSON error
  objects, redirects, non-200, truncated/oversized output fail closed. No remote
  endpoint option, `/load` calls, ffmpeg conversion or temporary audio files.

An installation rejecting flags or unable to load the pinned GGML model returns
a sanitized compatibility/startup error. Engine logs/audio are not attached.

## Local HTTP trust and lifecycle

Upstream provides **no authentication**, including its `/load` route, and permits
cross-origin requests. Loopback is not a multi-user security boundary. The random
256-bit route mitigates accidental/blind requests but is **not authentication**:
process arguments can reveal it. Untrusted local users/processes may discover it,
consume resources, interfere with inference or change the loaded model. This is
why `AllowLocalHTTP` is a mandatory explicit opt-in. Do not enable on untrusted
multi-user hosts; retain on-demand transcription there. There is no claim to
sandbox a malicious installed executable, its libraries or same-UID processes.

The adapter chooses/relinquishes an ephemeral IPv4 loopback port because upstream
cannot inherit a listener. To close the release/rebind race, **every connected TCP
socket is checked before sending any HTTP bytes**: its reverse established tuple
in bounded `/proc/net/tcp` must identify an inode held in the direct worker's
`/proc/<pid>/fd`. A foreign listener receives no WAV/request bytes. Acceptance can
be asynchronous, so ownership verification polls briefly with cancellation; it
fails closed when /proc is unavailable/restricted or unusually large (>4 MiB).
There is no fallback to an unverified endpoint. Port collisions may cause a safe
startup error; retry a later call. This requires the worker to listen directly,
not delegate accepting connections to a different process.

The private HTTP transport never uses inherited HTTP_PROXY/ALL_PROXY, never
follows redirects or decompresses responses, caps response headers at 16 KiB,
and reads at most MaxOutputBytes+1 body bytes. A trusted executable and model are
resolved/checked as regular files and pinned through inherited Linux `/proc` FDs;
the model must have GGML magic. Pinning prevents pathname substitution, not edits
to an already-open inode: keep installed files and their libraries trusted and
unchanged during sessions. Process groups are killed on failure, Close and worker
exit, and direct children reaped. Linux Pdeathsig additionally kills the direct
worker if its spawning OS thread/daemon dies unexpectedly; it does not guarantee
descendant cleanup after an abrupt daemon death. `exec.Cmd.WaitDelay` bounds pipes
held by descendants. Descendants deliberately escaping the process group are
outside the trusted-engine threat model.

## Acceleration reporting

`Device=cpu` explicitly sends `--no-gpu`; Info.Backend is `cpu` once started.
`Device=auto` leaves selection to the installed engine and reports `unknown`:
CUDA/Vulkan availability or actual inference use cannot be established from a
filename, configured choice, startup success or unstructured logs. Installed
CUDA/Vulkan-capable builds may accelerate this mode; no drivers are installed.
Explicit `Device=gpu` returns an actionable unsupported error **before I/O**,
because upstream's JSON API cannot prove GPU use or exclude silent CPU fallback.
No claim of verified GPU acceleration is made.

## Deterministic verification

`go test ./internal/voice`, `go test -race ./internal/voice`, and
`go vet ./internal/voice`. Fake installed Go processes validate flags, pinned
model access, multipart bytes, exact JSON text, model reuse, CPU flag/unknown
auto status, serialization, queued/active/startup cancellation, Close/reap and
process-group cleanup, disabled/no-consent validation, oversized diagnostics and
responses, malformed responses, redirects, HTTP failure, proxy bypass and
foreign socket rejection before any request bytes.
