# Optional resident live captions

`voice stream` is separate from legacy `enable`/`listen`/file transcription. Their
whisper-cli settings and all-utterance saving semantics are unchanged. The new
action supplies revisable captions via private read-only Unix IPC, optionally
saving raw notes after **final-confirmed transcript-prefix activation**. It is
not an acoustic wake model or general spoken command router.

## Opt in and run

Use Linux, a trusted already-installed whisper.cpp **whisper-server**, compatible
GGML model and PipeWire recorder. GinkGo downloads/installs nothing. Read the
[resident adapter's local HTTP trust warning](../internal/voice/README_resident.md#local-http-trust-and-lifecycle)
before consenting: its unauthenticated loopback server is unsafe with untrusted
local users/processes. Merge these tables into your existing config (do not
duplicate existing tables), build matching binaries and restart your daemon:

```toml
[voice]
enabled = true

[voice.stt]
executable = "/absolute/path/to/whisper-cli"  # legacy commands only
model = "/absolute/path/to/compatible-ggml-model.bin"
language = "en"                            # choose for your installed model
device = "auto"                            # unknown actual backend, NOT confirmed GPU
threads = 4
timeout = "5m"
residency = "on_demand"                     # legacy policy; leave unchanged

[voice.events]
enabled = true                             # default false

[voice.stream]
enabled = true                             # default false
executable = "/absolute/path/to/whisper-server"
allow_local_http = true                     # explicit security consent, default false
update_interval = "500ms"                   # audio cadence, not a latency guarantee
window = "5s"                              # rolling partial window

[voice.trigger]
phrase = "ginkgo take a note"               # optional default for --trigger
```

Existing [capture settings](voice-listening.md#setup) select the recorder, target,
silence endpoint, threshold and maximum duration. The stream command's flags are
`--save`, `--trigger`, `--namespace`, and `--output`; it does not accept AI or note
expansion options. It always creates raw new notes without calling AI/providers.

```sh
ginkgo-cli voice watch --output json  # observer only; can connect while idle
# In another terminal:
ginkgo-cli voice stream               # captions, NO notes
ginkgo-cli voice stop
ginkgo-cli voice stream --save --trigger "ginkgo take a note" --namespace work
ginkgo-cli voice status --output json
ginkgo-cli voice stop                 # finalize accepted audio, drain, release model/mic
# Or voice cancel / voice disable: discard pending work and join workers.
```

`stream` acknowledges admission and detaches from the initiating CLI connection.
Recorder/model/server startup failures subsequently fault the job: inspect events
and status. A daemon restart starts idle. It shares the exclusive microphone-job
slot with legacy capture. Disabled voice, status and subscriptions never start
audio or models. `--save=false` never writes a note, even with a configured trigger.
`--save` requires a nonempty literal phrase. Use legacy `voice enable` if you
explicitly want every completed utterance saved.

## Activation and exact provenance

- Match complete **leading** words only, case-insensitively with conservative
  punctuation/space normalization. No fuzzy/substring match or internal deletion:
  "please ginkgo take a note ..." does not match the example phrase. Hyphen and
  apostrophe compounds do not match a shorter last trigger word.
- Partials are captions only: they never authorize saving or light provisional
  recording intent. A final revision removing a phrase saves nothing.
- Phrase plus dictation in one utterance saves exactly one raw note. Only its
  leading control phrase/separators are excluded from the body; the **exact full
  final transcription**, including the trigger, remains in voice provenance.
- A final phrase alone activates `recording_note`, waiting at most five seconds
  for one future qualifying utterance (at least 150ms above threshold). Activity
  from shorter discarded noise pulses does not consume the arm or extend its
  deadline. Once a qualifying utterance is selected, the initial wait timeout no
  longer applies; ordinary capture duration/stop/cancel bounds still apply.
  That state is visible before the subsequent
  utterance's inference. Already-observed utterances while trigger recognition was
  pending are conservatively not selected retroactively. The next utterance's
  exact final text is its provenance; trigger-only audio is not a note.
- Recognition is required: same-breath `recording_note` appears only **after final
  STT**, before final/result publication and saving. There are no phrase/word
  timestamps or acoustic-immediacy claims. With a phrase alone, pause and wait for
  recording state before dictating. This intentionally trades activation latency
  for confirmed content selection.
- Two-second cooldown follows completion/cancel/timeout. Empty content,
  trigger-only timeout, stop while waiting, cancel and capture failure create no
  empty note. No pre-trigger utterances, partials, overlapping-window concatenation
  or raw audio are persisted. Already committed notes are not undone by cancel.

Raw stream notes are independent of AI. Existing [remote AI disclosure](voice-listening.md#commands)
still applies to deliberate legacy AI workflows; sync encryption does not protect
separate AI provider requests.

## External apps and bounded scheduling

The [NDJSON v1 contract](../internal/voiceevents/README.md) specifies fields, privacy,
socket derivation and reconnect rules. Connect a Unix stream and **send zero
bytes**; keep its write half open. `ginkgo.sock` derives `ginkgo-voice.sock`.
Initial state contains no history. Subscribers can remain connected while capture
stops and see idle transitions. Events currently describe **stream jobs only**;
legacy jobs do not emit partials or update stream state. Use ordinary `voice status`
for legacy jobs.

Each transcript revision replaces the prior text/window for its namespaced
utterance ID. Never append overlapping partials. `audio_start_ms`/`audio_end_ms`
are sample-derived offsets from that utterance's onset including pre-roll;
nonzero start means a clipped rolling tail. `final:true` uses the **original full
utterance WAV**. Final transcription and `note.saved`/`note.error`/`note.canceled`
are separate. Result `note_session_id` is original attribution, independent of
current `recording_note`/`active_note_session_id`. Caption clients cannot save or
control recording through this endpoint.

The recorder promptly enqueues while one worker serializes resident inference
and saving. Pending previews coalesce to one latest window. Four final utterances
may wait, plus one in-flight request. Finals remove obsolete pending previews;
no partial result follows its published final. Overflow, **including a stop flush
at capacity**, explicitly faults and cancels pending work rather than silently
dropping note audio. Slow event clients disconnect instead of blocking capture.
Cancel/shutdown joins recorder, inference, timeout, saving and resident-process
cleanup before acknowledging/releasing storage. Device-loss/stall finals are
rejected **before inference/persistence**. Repair and restart explicitly; no
automatic reconnect/suspend recovery.

Model load is lazy on the first snapshot, retained for that stream session and
released on stop/cancel/error/shutdown. There is **no idle timeout**. CPU sends
`--no-gpu`; `auto` may use an installed accelerated backend but actual use is
**unknown**, never confirmed GPU. Explicit `gpu` and absent local-HTTP consent
fail before the microphone starts. This is repeated bounded-WAV inference, not
decoder/token streaming; no 500ms word-latency guarantee on arbitrary hardware.

## Verification and remaining work

Run the [repository checks](voice-listening.md#status-and-verification). Daemon
tests use synthetic PCM through a fake recorder, an injected fake resident and
real private Unix sockets: previews before silence, state/result ordering,
same/next-utterance gates, revised-away triggers, exact provenance,
caption-only/no-trigger isolation, queue overload, device-loss rejection,
subscription independence, cancellation and joined cleanup. Adapter tests use
fake installed processes. Existing black-box smoke scripts cover legacy behavior.
**No actual microphone, installed Whisper server/model, GPU, hardware inference or
provider was exercised for this live-streaming change.** Hardware quality/latency,
acoustic activation, word alignment, decoder streaming, idle eviction, AI-gated
stream notes and automatic device recovery remain unimplemented/unverified.
