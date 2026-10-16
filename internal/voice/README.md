# Optional local voice (POC)

Linux-only, local, **on_demand** whisper.cpp adapter. No AI or note-service
dependencies, downloads, wake word, persistent inference worker, or idle policy.
Configuration/CLI/IPC integration belongs to the caller.

## Optional microphone capture

`NewCapturer(CaptureConfig)` is a separate lazy Linux, CGO-free adapter. It
executes a trusted installed `pw-record --raw --format s16 --rate 16000
--channels 1 [--target TARGET] -` with explicit arguments, never a shell. No
device/version probes occur at construction or while disabled (`ErrDisabled`).
The transcriber and its existing API remain independent and unchanged.

```go
capturer := voice.NewCapturer(voice.CaptureConfig{Enabled: true})
err := capturer.Capture(ctx, voice.CaptureOptions{Once: true}, stop,
    func(segment voice.Segment) error {
        // Enqueue promptly into a bounded worker queue; never block on inference.
        // segment.WAV is a complete WAV, suitable for the existing transcriber.
        return queue(segment)
    })
```

- Config fields: `Enabled`, `Executable` (default `pw-record`), `Target`,
  `SilenceTimeout` (1.5s), `MaxDuration` (2m), `StartTimeout` (30s), `Threshold`
  (0.02 normalized RMS). `CaptureOptions` has `Once` and all fields except
  `Enabled`/`Executable`; nonzero options override config/defaults. Negative
  values are invalid; threshold must be finite and in (0,1], max duration is
  150ms–5m, silence timeout is at most 1m, and start timeout is at most 5m.
  Duration caps round down to whole 10ms frames, never below 150ms. Pre-roll is
  trimmed as needed to reserve at least 150ms of speech within the cap.
- Energy is measured in 10ms frames, with up to 200ms pre-roll and at least 150ms of
  above-threshold speech before emitting. This is simple RMS VAD, not semantic
  speech detection: loud noise may qualify. Tune threshold for the source.
- `Once: true` emits one qualifying utterance and returns; the initial timeout
  returns nil without emitting if no qualifying speech arrives. Continuous mode
  keeps **one recorder stream open between utterances**, with bounded pre-roll
  during idle silence. No initial speech timeout applies in continuous mode.
- `Segment.Reason` is `silence`, `max_duration`, or `stop`. Both sample count
  (including pre-roll/trailing silence) and wall time bound each utterance.
  WAVs are mono signed PCM16, 16kHz, at most 5 minutes (under 32 MiB).
- Closing/sending `stop` finalizes qualifying current speech, then returns nil.
  Context cancellation/deadline discards partial audio and returns the context
  error. Callback errors propagate; no partial audio is emitted on stream errors.
- EOF/truncated PCM/device loss are explicit errors. No complete frame for 5s
  is an actionable stall error, even when idle. Callers can restart capture.
- One active capture per adapter; queued calls respect cancellation and stop.
  Raw PCM buffering is bounded (eight queued frames plus one utterance); stderr
  is discarded, capped at 64 KiB per capture, and overflow aborts capture.
  Cleanup kills/reaps the recorder and kills same-process-group descendants on
  stop, cancellation, EOF, callback failure, timeout or overflow. As with the
  transcriber, a deliberately detached process is outside this trust boundary;
  no OS-level child RSS/CPU sandbox is provided.
- Tests use synthetic PCM and fake processes only, never a real microphone.

```go
transcriber := voice.NewTranscriber(voice.Config{
    Enabled: true,
    Executable: "/usr/bin/whisper-cli",
    Model: "/home/user/models/ggml-base.bin",
    Language: "auto", // or e.g. "en"
    Device: "cpu",   // "auto" permits the installed engine's default GPU/CPU behavior
    Threads: 4,
    Timeout: 5 * time.Minute,
})
transcript, err := transcriber.Transcribe(ctx, audioPath, voice.Options{})
// Optional per-request overrides: Options{Model: "/path/ggml-small.bin", Language: "fr"}.
// On err, do not create a note. Otherwise preserve transcript.Text exactly.
// If strings.TrimSpace(transcript.Text) == "", do not create a note.
```

`Transcript` has `Text`, optional detected `Language`, `Engine` (`whisper.cpp`),
and `Model` (resolved absolute model path). JSON segment `text` strings are
concatenated without added separators, trimming, timestamps, or rewriting. The
whisper-cli JSON file, not stdout/logs, is the source of transcription. No engine
version is inferred from filenames or invented in metadata.

## Installed-engine compatibility controls

Use a trusted installed **whisper.cpp whisper-cli**, not Python's `whisper` CLI.
The adapter requires `-m`, `-f`, `-l`, `-t`, `-oj`, `-of`, `-np`, `-nt`, and
`-ng` (for CPU), along with JSON shape:

```json
{"result":{"language":"en"},"transcription":[{"text":" exact segment text"}]}
```

`result.language` is optional; `transcription` and every segment's string `text`
are required. The engine must append `.json` to the `-of` prefix and open that
path normally, following its symlink to an inherited pipe. Engines using atomic
output replacement or requiring a seekable JSON destination are not compatible.
Model/audio `/proc/self/fd` paths must be supported by the installed build.

Verified on Linux CPU with whisper.cpp v1.7.6 (commit
`a8d002cfd879315632a579e73f0148d06959de36`), `ggml-base.en.bin` (SHA256
`a03779c86df3323075f5e796cb2ce5029f00ec8869eee3fdfb897afe36c6d002`), and upstream
`jfk.wav` through the real CLI/daemon preview and note-saving paths. Ordinary Go
tests still use fake executables, never real models or network. Other packaged
builds must be smoke-tested against these flags/schema/descriptor semantics.
There is no `--version` probe at construction or when disabled, and no automatic
feature/version fallback.
Missing/unsupported controls, malformed output, and engine failures are errors.
`Device: "gpu"` is rejected rather than falsely guaranteeing GPU execution;
`auto` may fall back to CPU. GGML magic is checked, but full model compatibility
and integrity remain the installed engine/user's responsibility.

## Bounds and lifecycle

- `NewTranscriber(Config)` does no I/O, validation, or spawning. Disabled calls
  return `ErrDisabled` before path or configuration probing.
- One inference per adapter, including request-deadline-aware queueing. Reuse one
  adapter for a daemon; creating many adapters bypasses this concurrency bound.
- Default timeout 5 minutes (the caller's earlier deadline wins).
- Nonempty regular RIFF/WAVE only: standard 16-byte PCM format chunk, signed
  16-bit, 16000 Hz, mono, correct rates/alignment/chunk lengths, one data chunk.
  No automatic conversion. Errors include an ffmpeg conversion example.
- Defaults: audio 32 MiB, duration 5 minutes, model 8 GiB, **each** of stdout,
  stderr, and JSON 1 MiB. Config fields: `MaxAudioBytes`, `MaxAudioDuration`,
  `MaxModelBytes`, `MaxOutputBytes`; zero selects defaults, negatives are errors.
  Hard ceilings: audio 1 GiB, model 16 GiB, each output 16 MiB. Threads: 1–256.
- Input symlinks are resolved; final-component replacement symlinks are rejected
  on open. Nonblocking opens plus inode checks reject FIFOs/devices/directories.
  The executable/model are passed as pinned descriptors. Audio is copied into a
  private temporary WAV after bounded validation, preventing subsequent path or
  content edits from changing inference input. Temporary WAV storage is bounded
  by the audio limit; JSON flows through a bounded pipe, not a growing disk file.
- Explicit exec argv; no shell. No engine diagnostics are included in errors,
  to avoid disclosing audio/model/engine log content. Output overflow returns
  `ErrOutputLimit`; cancellation/deadline errors support `errors.Is`.
- Cancellation SIGKILLs the child process group; remaining same-group descendants
  are also killed after completion. Pipes have bounded cleanup waits. A malicious
  child that creates a new session/process group can escape: this is not a sandbox.
- The executable and installed model are trusted local files. Descriptor pinning
  prevents path substitution **after open**, not in-place model/executable edits
  or hostile ancestor-directory mutation before open. No CPU/RSS/GPU quota is
  enforced beyond threads, deadline, file/output limits and adapter serialization.
  Local filesystem reads are bounded in size but are not interruptible kernel I/O.

Focused verification: `go test -race ./internal/voice` and
`go vet ./internal/voice`. Empty transcripts return success; this package never
creates notes, so the caller must enforce the empty/no-note rule.
