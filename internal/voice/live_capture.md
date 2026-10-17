# Continuous capture and note gating integration

These primitives do no inference, IPC, storage, executable/model downloads, or
device probing at construction. The old `Capturer.Capture` API is unchanged.

## Live captions

Reuse one `Capturer` and call:

```go
err := capturer.CaptureLive(ctx, voice.LiveCaptureOptions{
    UpdateInterval: 500 * time.Millisecond,
    WindowDuration: 5 * time.Second,
    ObservePCM: enqueueDetectorAndNotePCM, // optional, including idle frames
}, stop, enqueueCaptionEvent)
```

- Live and legacy capture share one admission slot and the same bounded
  recorder lifecycle. Only one `pw-record` process runs per capturer. Use live
  capture for both captions and note audio; never open a second microphone.
- PCM is 16 kHz mono signed PCM16 little-endian. Every stream starts its sample
  clock at zero. `PCMChunk.StartSample` and event `[StartSample, EndSample)` are
  absolute **sample** offsets. Namespace capture-local utterance/session IDs with
  an integration-owned stream ID, especially after device restart.
- `partial` events contain bounded complete WAV snapshots, default 5s (maximum
  30s), every approximately 500ms of captured audio (configurable 10ms–5s).
  Snapshots need 150ms accumulated above-threshold audio, and another partial is
  useful only after new above-threshold audio. They arrive before silence.
- **Partial transcripts are revisions, never append-only word deltas.** Use
  stream ID + utterance ID + revision + window offsets. A nonzero start offset
  can indicate a rolling tail, not a full-utterance transcript. Late inference
  results must not overwrite a newer revision or final. Seam assembly/alignment
  of rolling-window transcripts belongs to the STT/visualization integration.
  `UtteranceStartSample` retains the absolute utterance onset (including pre-roll)
  across rolling windows, allowing exact utterance-relative offsets even when the
  first useful window already clips the onset.
- `final` contains the whole bounded utterance WAV, including pre-roll/trailing
  silence, rather than only its rolling tail. Sample and wall-time duration
  bounds are inherited from capture settings (default 2m, hard maximum 5m).
  Silence/max-duration split utterances without restarting the microphone.
- `activity` is audio-free level/state telemetry at the update cadence when no
  useful partial exists. `Speech` and `Level` are frame RMS, **not** semantic
  speech/wake detection. Idle telemetry has utterance ID zero. `ObservePCM`, if
  supplied, receives owned 10ms PCM chunks even during silence, suitable for a
  separately bounded detector queue and the note gate.
- Callbacks are synchronous and **must promptly enqueue, not infer/block**.
  Coalesce queued partials, but preserve finals with reserved capacity or
  fail explicitly. Callback errors abort capture and propagate; there is no
  promise of another callback/final after a callback error. Callback consumers
  own their snapshots; retaining unlimited snapshots would bypass memory bounds.
- Stop flushes qualifying **already consumed** PCM with reason `stop`; queued
  frames and unread process/kernel buffers are outside this boundary. Context
  cancellation discards the active utterance and returns its context error.
  Unlike legacy capture, live device loss/stall first emits qualifying consumed
  audio with `device_lost`/`stall`, then returns the stream error. This final can
  support recovery, but it is not authorization to save background speech.
- Recorder stderr is discarded/capped at 64 KiB; PCM read-ahead is eight frames;
  there is one bounded utterance plus at most 200ms idle pre-roll. Each partial
  allocation is window-bounded. Stop/errors/cancel kill/reap the process group.

## Note sessions (independent of captions)

`NewNoteSession(NoteSessionConfig{Enabled: true})` constructs an in-memory,
single-owner gate. It never calls a note service. Serialize all its methods,
including detector/STT completions, and supply a monotonic `time.Time` clock.
Create a fresh gate for each new capture stream; offsets must remain contiguous.

1. Feed **all** captured PCM into `FeedPCM(now, chunk)`, including while `armed`.
   It retains only bounded RAM detection history (default 5s, maximum 30s).
   No trigger means no recording note, even if captions recognize background
   speech all day. Disabled gates return `ErrDisabled` before inspecting input.
2. An installed trained detector can implement `AcousticTrigger`. Its detections
   name the exact phrase's `[StartSample, EndSample)` in the capture clock.
   `Trigger(now, detection)` activates only while armed, outside cooldown.
   It copies history **from phrase start**, never earlier background speech,
   including any dictation spoken in the same breath before async detection
   completed. Expired history is an explicit `ErrTriggerExpired`, not silent
   truncation. Configure history for expected detector/inference latency.
3. `ConfirmDictation(sample)` requires verified non-trigger content at/after
   `DictationFrom` (the phrase end). Do not confirm on the wake phrase, RMS alone,
   or provisional transcript text that may be revised away. Live final WAVs
   remain available for independent STT verification before ending a note gate.
   The gate does not strip text or guess word timestamps. The integration must
   use a provider with adequate phrase/content boundaries or explicitly resolve
   text boundaries after final recognition.
4. A trigger alone cancels after 5s by default (configurable to 30s); schedule
   `Tick(now)` regularly even when no audio arrives. Confirmed dictation removes
   this initial timeout. Manual `Begin(now)` starts at the next sample, bypasses
   acoustic cooldown, and is not subject to the trigger-only timeout; it still
   needs verified content before a result can be saved.
5. `End(now)` handles manual end/stop/utterance endpoint; `Cancel(now)` discards
   note audio without stopping captions. `Tick` and PCM sample count enforce the
   note maximum (default 2m, hard 5m). A finalizing result owns a complete WAV;
   a canceled/trigger-only result has no WAV. Require valid **nonempty final
   text** before storage even for a finalizing result. Capture cancellation
   should call `Cancel`, never `End`.
6. Handle the returned result exactly once, then `Rearm()`. Finalizing/canceled
   states cannot auto-reactivate. Cooldown (default 2s) begins at completion;
   rearming preserves both cooldown and a consumed-sample watermark, rejecting
   delayed duplicate detections. Manual begin may deliberately override cooldown.

No acoustic provider/model is installed or bundled. Automatic acoustic activation
therefore remains unavailable until the caller injects a real provider. No fuzzy
or literal transcript trigger adapter is implemented in these primitives: text recognition is not an
acoustic wake model, and partial revisions cannot safely authorize deletion or
activation by arbitrary substring matches. Manual sessions work without a wake
provider. STT model residency/GPU selection and event-queue policy are separate
integration concerns, not implemented by capture or note gating.
The daemon's separate [`voice stream`](../../docs/voice-stream.md) integration
implements final-confirmed literal-prefix gating, not acoustic activation, and
owns resident inference, bounded scheduling, IPC and final-only note persistence.

Tests use deterministic synthetic PCM, caller-driven gate clocks, and fake
recorder processes, never microphones/models/network. Run `go test -race
./internal/voice` and `go vet ./internal/voice`.
