# Hey Ginkgo: continuous wake-gated voice notes

The optional Linux Sherpa backend supports **"Hey Ginkgo" → dictation → saved
raw note** with the attested native profile below. After explicitly enabling both
voice and streaming, start it with:

```sh
ginkgo-cli voice stream --save --trigger GINKGO
```

The real CLI, daemon, native CPU helper, public events and private SQLite storage
have been exercised together with recorded audio, not just helper candidates or
fake ASR. The frozen detector's accepted 5/6 natural wake result is unchanged;
missed wakes do not authorize notes. See [verification](#verification-status) and
the [helper safety details](../internal/sherpavoice/README.md).

This is **opt-in**. `voice enable`, `voice listen`, and file transcription keep
their separate Whisper/AI behavior; **`voice enable` is not the Sherpa start
command**. The default stream backend remains `whisper`. Listening has no idle
cutoff, but each run is limited to eight hours or 4096 utterances/selected sessions;
stop/restart explicitly before either bound. Daemon restart returns idle, not
automatically resumed capture. No configuration or service is changed by packaging.

## Install explicitly; never download at runtime

The main CLI/daemon stays pure Go/CGO-free. Only the separately built
`cmd/ginkgo-voice` helper needs Go CGO, a C/C++ toolchain, **sherpa-onnx v1.13.8**
with its C API enabled, and matching ONNX Runtime headers/shared libraries.
Do not mix C API headers, a different Sherpa version, or an unrelated ONNX
Runtime installation. The local packaging target below uses **CPU ONNX Runtime
1.28.2** and pinned, unmodified Sherpa source revision
`11afbd009a7f8c08f4bcf2fc1b265d0df4670fbf`. It cannot acquire CUDA by changing
configuration. CUDA is unavailable in this set: prefer explicit `provider = "cpu"`;
auto/CUDA may use only the existing explicit CPU fallback policy, not claim GPU
success. Ordinary `make build` explicitly uses `CGO_ENABLED=0`; native helper
packaging is separate from `build`, `install`, `run`, and service setup.

### Build a local, relocatable binary set

Use already provisioned native assets; neither command downloads dependencies or
models. Go dependencies must already be cached (`GOPROXY=off`,
`GOTOOLCHAIN=local`). A C/C++ toolchain, CMake, Go, Git, binutils, and Linux
`ldd`/coreutils are required. Choose a **fresh output outside the source tree**:

```sh
make voice-bundle \
  SHERPA_SOURCE="/trusted/sherpa/source" \
  SHERPA_BUILD="/trusted/sherpa/build" \
  BUNDLE_OUTPUT="/absolute/fresh/ginkgo-bundle"

# Helper plus its runtime libraries only:
make voice \
  SHERPA_SOURCE="/trusted/sherpa/source" \
  SHERPA_BUILD="/trusted/sherpa/build" \
  VOICE_BUNDLE_OUTPUT="/absolute/fresh/ginkgo-helper"
```

The source/build pair lets the script compile the included
`internal/sherpavoice/nativebridge` as GinkGo's `ginkgo-kws` C ABI shim. The CMake contract
is target `ginkgo-kws`, output `lib/libginkgo-kws.so`. Optional `SHERPA_PREFIX`
and `ORT_PREFIX` still override the installed C API/ORT headers and shared
libraries, but **the old prefix-only invocation is no longer sufficient**: the
shim needs the matching pinned source and static build dependencies. ORT's prefix
must include `VERSION_NUMBER`, `LICENSE`, and `ThirdPartyNotices.txt` as well as
`include/` and `lib/`.

The complete output contains:

- `bin/ginkgo-cli`: CGO-free CLI/daemon; `bin/ginkgod -> ginkgo-cli`.
- `bin/ginkgo-voice`: **real CGO** executable (`sherpa_onnx` tag), not a stub.
- `lib/libginkgo-kws.so`, `libsherpa-onnx-c-api.so`, `libonnxruntime.so`.
- `BUILD-MANIFEST.txt`, `SHA256SUMS`, dependency/help checks, and upstream licenses.

The script builds the shim in an output-owned temporary directory, checks native
ELF linkage and actual `ldd` resolution for every DSO without `LD_LIBRARY_PATH`,
and smoke-tests `--help` without microphone/model initialization. A missing
non-system transitive dependency fails packaging rather than silently shipping an
incomplete set. Linux's loader, glibc, libstdc++, and libgcc remain host prerequisites;
this is not a cross-distribution static native bundle. The helper uses an
`$ORIGIN/../lib` executable rpath and the shim uses `$ORIGIN`; the upstream C API
already searches `$ORIGIN` first. Existing upstream binaries are copied unchanged,
so upstream debug strings or unused fallback build paths may remain; dependency
checks must still resolve the packaged libraries locally.

Outputs are never overwritten. Failure removes only this attempt's staging
directory. Success publishes by a same-filesystem rename. For an optional CGO-free
LSP, call `bash scripts/build-voice-bundle.sh` with the same paths plus `--with-lsp`.
The manifest records revisions, versions, and build settings; hashes identify the
actual binaries/libraries. This is a repeatable local recipe, not a claim of
bit-identical upstream builds across different toolchains.

Move the **whole directory**, keeping `bin/` and `lib/` together, then verify:

```sh
cd "/absolute/path/to/ginkgo-bundle"
sha256sum -c SHA256SUMS
env -u LD_LIBRARY_PATH -u LD_PRELOAD ldd bin/ginkgo-voice
bin/ginkgo-cli --help
bin/ginkgo-voice --help
```

Packaging does not launch a daemon or install/restart a service. Choose the
absolute bundled `bin/ginkgo-voice` path in your configuration, then deliberately
start the **bundled `bin/ginkgod`**, not an older installed daemon. If a daemon
already owns the socket, stop it using your usual procedure first; do not start
a second daemon on that socket. Loader/help checks alone are not inference or
note-persistence tests. The [run instructions](#run-and-inspect) keep these
activation steps explicit.

Install a trusted `pw-record` and a trusted helper executable yourself. Native
code and models execute locally with the daemon's privileges; do not point these
settings at untrusted binaries. Build/packaging commands never install drivers or
models, download assets, start the helper, or enable capture at startup. An enabled,
explicit `voice stream` request is what launches the helper and capture.

Acquire these model assets manually from the Sherpa upstream model documentation:

- English **Parakeet TDT 0.6B v2 FP32**, encoder, decoder, joiner and matching tokens.
  Use FP32 v2 for both CPU and CUDA initially; neither quantized assets nor a
  similarly named v3/model architecture are interchangeable.
- The frozen **zh-en3M2025-12-20 FP32 epoch13/avg2/chunk16/left64** KWS encoder,
  decoder, joiner, tokens, and **compiled keywords file**. Other KWS models or
  arbitrary keyword spellings are not supported by the attested native adapter.
- A compatible Silero VAD ONNX model.

Models stay external to the binary set. Parakeet FP32 (~2.5 GB, including the
encoder's external `encoder.weights` data) and Silero require explicit provisioning
permission; filenames alone do not prove completeness. Preserve external
ONNX weight sidecars beside their graph. The frozen KWS model is
`sherpa-onnx-kws-zipformer-zh-en-3M-2025-12-20`, with compiled keyword phones
`HH EY1 G IH1 NG K OW1 @GINKGO`, representing the full spoken **"Hey Ginkgo"**,
not just "Ginkgo". KWS uses fixed beam16, score1, threshold0.25, trailing1 and
CPU/one thread. Native initialization verifies the hash-bound KWS profile and
owned integer-clock bridge; its emission grid is not a phonetic word-end claim.
The accepted 5/6 wake result is not a promise of perfect recall. No further model
tuning or automatic model fetching is part of packaging.

For the approved local staging set, verified model downloads are now available at
`/tmp/opencode/ginkgo-sherpa-build/models/parakeet-v2-fp32/` and
`/tmp/opencode/ginkgo-sherpa-build/models/silero/silero_vad.onnx`. Parakeet is pinned
to Hugging Face revision `86891485dd8ad7cb28cb1aade45c3e23d0197c30`
(2,512,987,040 bytes including sidecars); Silero is 643,854 bytes. Preserve their
`provenance.json` source URLs/checksums with the external assets. Availability and
download checksums alone do not assert a successful native ASR or microphone test.

The bundle includes `voice-sherpa.example.toml` with capture **disabled**. To fill
in the approved external paths while packaging (no copying or downloads), export:

```sh
export VOICE_MODELS_DIR="/tmp/opencode/ginkgo-sherpa-build/models"
export VOICE_KWS_DIR="/tmp/opencode/ginkgo-sherpa-native/phoneme-validation/sherpa-onnx-kws-zipformer-zh-en-3M-2025-12-20"
export VOICE_KEYWORDS_FILE="/tmp/opencode/ginkgo-sherpa-build/models/kws-hey-ginkgo.txt"
```

Use the existing frozen compiled keyword file, not an invented filename or a new
tokenization run. The example selects FP32 chunk-16 KWS graphs. Paths alone do
not verify models: native initialization separately enforces the KWS hashes;
retain/verify Parakeet and Silero provenance yourself. Update the helper path after
relocating the bundle. Never replace existing user configuration with the example
wholesale. To activate deliberately, merge the settings, change **both**
`voice.enabled` and `voice.stream.enabled` from `false` to `true`, and use `--save`
as shown below. The shipped example never enables microphone capture itself.

Keep each archive's origin/version, read the model's own license and usage terms,
and verify publisher checksums before extraction. Where an upstream checksum is
not published, record a locally verified SHA-256 manifest and pin the exact
artifact URL/revision for reproducibility. GinkGo does not claim to verify model
identity from filenames, compile arbitrary phrases, or distribute model licenses.
Never mix graph/token files from different model bundles. No automated download
or license acceptance is provided.

## Configure

Copy and adapt this example yourself; never overwrite an existing config. Merge
into existing tables; all model paths must be explicit absolute installed
paths. Executables may be installed absolute paths or trusted PATH names; the
daemon resolves them to absolute paths **only on an enabled stream request**.

```toml
[voice]
enabled = true

[voice.events]
enabled = true

[voice.stream]
enabled = true
backend = "sherpa"
update_interval = "500ms"
window = "5s"

[voice.capture]
executable = "/absolute/path/to/pw-record"
target = ""                       # default PipeWire target
silence = "1.5s"
# max_duration remains a legacy setting; Sherpa uses its own bound below.

[voice.sherpa]
executable = "/absolute/path/to/ginkgo-bundle/bin/ginkgo-voice"
provider = "cpu"                   # this binary set contains CPU ORT only
cpu_fallback = true                # opt out to fail instead of falling back
keyword = "GINKGO"                 # installed compiled label; empty selects all
max_recording = "60s"              # wake + pause + dictation; independent of legacy 2m
keywords_file = "/models/kws-hey-ginkgo.txt"
silero_model = "/models/silero/silero_vad.onnx"
startup_timeout = "2m"             # each pre-capture initialization attempt, <=5m
shutdown_timeout = "30s"           # selected-note finalization + exit, <=5m
trigger_timeout = "3s"             # short post-wake wait for dictation, not recording limit
cooldown = "1s"                    # suppress duplicate wakes; do not wait before speaking
pre_roll = "500ms"                 # bounded leading context; 0..30s

[voice.sherpa.parakeet]
encoder = "/models/parakeet-v2-fp32/encoder.onnx"
decoder = "/models/parakeet-v2-fp32/decoder.onnx"
joiner = "/models/parakeet-v2-fp32/joiner.onnx"
tokens = "/models/parakeet-v2-fp32/tokens.txt"

[voice.sherpa.kws]
encoder = "/models/zh-en3M/encoder-epoch-13-avg-2-chunk-16-left-64.onnx"
decoder = "/models/zh-en3M/decoder-epoch-13-avg-2-chunk-16-left-64.onnx"
joiner = "/models/zh-en3M/joiner-epoch-13-avg-2-chunk-16-left-64.onnx"
tokens = "/models/zh-en3M/tokens.txt"
```

The names above are illustrative, not model identity checks. Preserve actual
bundle filenames. Sherpa does **not** require `voice.stream.allow_local_http`,
`voice.stt.model/device/residency`, or a Whisper executable. The resident helper
uses inherited private NDJSON pipes, not an HTTP server. KWS and VAD run on CPU;
the requested provider is for Parakeet. The capture threshold setting belongs to
legacy capture; Sherpa uses acoustic KWS and Silero VAD instead.

## Run and inspect

Use the same explicit configuration for both processes, with an already configured
default namespace (or pass `--namespace` for another configured namespace).
After manually merging/enabling the settings above, choose these paths in each
terminal. Starting this daemon is an explicit operator action, not an install step:

```sh
BUNDLE="/absolute/path/to/ginkgo-bundle"
CONFIG="/absolute/path/to/your/config.toml"

# Terminal 1: use the NEW bundled daemon; do not leave an old daemon on the socket.
"$BUNDLE/bin/ginkgod" --config "$CONFIG"
```

In another terminal, set the same `BUNDLE` and `CONFIG` values, then:

```sh
"$BUNDLE/bin/ginkgo-cli" --config "$CONFIG" voice stream --save --trigger GINKGO
"$BUNDLE/bin/ginkgo-cli" --config "$CONFIG" voice status --output json

# Optional read-only observer in another terminal; Ctrl-C stops only the observer.
"$BUNDLE/bin/ginkgo-cli" --config "$CONFIG" voice watch --output json

# Deliberately stop listening and finalize an existing selected note:
"$BUNDLE/bin/ginkgo-cli" --config "$CONFIG" voice stop
# OR discard pending selected work and stop listening:
"$BUNDLE/bin/ginkgo-cli" --config "$CONFIG" voice cancel
```

Wait for status/events to show `backend: "sherpa"` and
`effective_provider: "cpu"`; stream admission returns before model initialization.
Say **"Hey Ginkgo"**, then dictate in the same breath or after a short pause
(about two seconds is supported). Do **not** wait for the one-second cooldown:
it suppresses duplicate wakes, not dictation. The default three-second
`trigger_timeout` expires a wake awaiting dictation, anchored to actual completed
audio rather than ASR return or VAD hangover. Once dictation begins, recording
continues until the default **1.5s silence** or configured maximum (default 60s,
including wake/pause). Standalone/repeated wake-only speech creates no note.
`note.saved` confirms persistence; the current raw body retains the spoken control
phrase exactly as transcribed. No AI rewriting or guessed prefix trimming occurs.

The listener stays available for subsequent wakes until explicitly stopped or a
session bound/fault is reached. Stop/restart before **eight hours or 4096
utterances/selected sessions**. A daemon restart is idle: issue the `--save`
command again. For captions only, omit `--save`; that command cannot create notes.

`--trigger` selects a **compiled keyword label**, not arbitrary words to tokenize.
Labels accept letters, digits, `_`, `.`, `-` (at most 128 bytes). Omit it to use
`voice.sherpa.keyword`. Explicit `--trigger ""` also uses the daemon's configured
label; it does not override a nonempty configuration to select all. Only an
empty **configured** label selects all installed keywords.
Whisper's literal transcript-prefix mode and `voice.trigger.phrase` are unrelated.
`--save=false` cannot save notes, even if a malfunctioning helper attempts it.
Only raw new notes are supported; AI, append/expand and per-request legacy model,
language or capture overrides are rejected. English is the fixed model language.

Status adds `backend`, `helper_engine`, `requested_provider`, `effective_provider`,
and `provider_fallback_reason` to JSON/protobuf. Effective provider starts
`unknown`; only validated **ready** evidence updates it. `cuda` means CUDA EP was
available and initialized under the linked ORT, **not** that every graph node ran
on GPU. CPU fallback reports `cuda_unavailable`, `cuda_failed`, or
`cuda_init_failed`; never infer provider from a model filename. Auto/cuda with
fallback disabled must not silently become CPU.

The init/ready/start handshake forbids capture while loading models. A native
startup crash/ready timeout may retry CPU **once before accepted ready**, only
when allowed. Cancellation/stop prohibits retry; nothing retries after ready or
replays already selected notes. Admission returns before initialization: inspect
status/events for later startup failure. A restart is always idle.

## Selection, provenance, events and bounds

One helper owns one `pw-record` stream and resident models. Re-decoded rolling
windows supply revisable live previews; this is not decoder/token streaming and
there is no fixed latency promise. An admitted acoustic wake changes
`recording_note` independently of ASR finalization. When a later wake shares an
unfinished VAD segment, one bounded deferred selection preserves ordering: the
old candidate/cancellation precedes the next public wake, without mixing control
phrases or blocking capture on ASR. The selected session retains the
original selected audio across a wake/pause/dictation sequence until finalized or
canceled; a later ambient utterance cannot substitute for it.

Ambient captions, including finals, **never authorize saving**. Only a validated
final selected candidate tied to its wake session/utterance/sample origin can
create one note. Its exact selected-audio transcript becomes `OriginalText` in
provenance, with trusted `sherpa-onnx` / `parakeet-tdt-0.6b-v2` / `en` metadata
rebound by the parent. **The current helper retains the control phrase in the
body (`Body=Original`)**: KWS detection timestamps cannot safely identify its
exact acoustic end. The bridge supports a proven exact prefix/control span, but
this helper does not invent one. Empty/control-only/trigger-only input must
cancel, not create a note.
Selected ASR must also begin with the full trusted phrase (or pinned Parakeet's
observed spelling "Hey Ginko"); prefix misses and unknown spoken forms cancel.
This is an additional veto, never ASR-only wake authorization. A compiled label
such as `GINKGO` is not itself assumed to be spoken text.
Raw audio and ambient captions are not stored. Already committed notes are not
undone by a later cancellation.

The [public NDJSON v1 schema](../internal/voiceevents/README.md) is unchanged and
`voice watch` keeps its existing flags/behavior. Revisions replace earlier text;
never append overlapping windows. For this backend audio offsets are sample-derived
milliseconds since the helper's capture epoch. Ambient captions intentionally carry
no save-session authority. A selected candidate also publishes its distinct exact
final transcript with its original session attribution; that observation is not
save authorization. `note.saved`, `note.error`, and `note.canceled` carry the
selected note session ID independently of current global recording state: ASR/save
may finish after recording has turned off. No native stderr/model paths reach
public errors.

Update cadence is 10ms..5s, rolling window 150ms..30s and no shorter than update;
silence is <=1m and <=selected maximum. Selected audio is >=250ms; the exact
helper ring budget is `max_recording + max(pre_roll, window) + silence + 1s <=120s`.
The default 60s selection fits; a configured 2m does **not**, and is rejected
before launch rather than clipped. The legacy 2m capture default is unchanged
and does not select the Sherpa maximum. The daemon retains
at most four bounded text candidates plus one in-flight save, not audio buffers.
Overflow/fault clears queued candidates. Slow watchers disconnect rather than
blocking microphone reads. Stop drains finalized selected candidates; cancel,
disable and shutdown discard pending candidates and wait for the supervisor and
the owned save worker before acknowledging or releasing storage/mic exclusivity.
Late callbacks from an old run are revoked. Helper/recorder process groups are
joined by the supervisor. No automatic device reconnection is attempted.

The native bridge has a hard **eight-hour capture lifetime** and bounds each
epoch to **4096 utterances/selected sessions**. Explicitly stop and restart the
stream before reaching either limit; no rollover or automatic resume is provided.
Silence alone does not impose an idle timeout or release resident models.
Daemon restart always returns to idle: rerun `voice stream --save --trigger GINKGO`
to resume wake-gated notes. Recording consumes the same exclusive job slot as
legacy stream/listen/enable. This is continuous listening within a bounded run,
not unattended, unbounded 24/7 capture.

## Verification status

Real CPU inference and **actual SQLite note persistence** passed through a freshly
built CLI/daemon and real native helper, using paced, full recorded files instead
of a microphone. Each case used private HOME/XDG/config/data/socket paths; the
default **1.5s silence**, **3s post-wake wait**, and **500ms pre-roll** were retained
(30s selected maximum for these short recordings):

| Real CLI/native case | Persisted notes |
| --- | ---: |
| Original full recording: four dictations plus standalone wake | 4 |
| Natural full recording: two detected dictations; standalone wakes, missed wake and confusables excluded | 2 |
| Full original without `--save` | 0 |
| Ambient/other-trigger negative recording | 0 |
| `voice stop` during selected dictation | 1 |
| `voice cancel` during selected dictation | 0 |

Every saved note matched its exact selected-final transcript, `OriginalText`,
trusted engine/model/language metadata and uniquely attributed public saved
session; direct SQLite counts matched exported entries and saved events exactly.
Stop drained selected work, cancel prevented late persistence, and helper/recorder
processes were joined. Full recordings preserved earlier wake context: no note
was borrowed from a standalone wake for later missed/ambient speech. An initial
1.5s-VAD retrigger loss was fixed and all six cases rerun without shortening silence
or relaxing expected counts. Audio, actual note bodies and detailed results stay
outside Git.

Additional tests use fake installed helpers over actual pipes, private Unix IPC
and temporary storage: pre-silence captions, wake state, selected provenance,
ambient/no-save isolation, provider ready/fallback/retry, pre-ready cancel, EOF and
capture failure, graceful stop, queued-candidate discard, and cancellation joined
with deliberately blocked persistence. Bridge tests cover strict protocol and
process-group cleanup. Existing Whisper tests and voice smoke scripts remain
applicable. **No real microphone, GPU, driver changes or external provider were
used.** Recorded CPU validation is not a microphone-device, long-running soak,
latency benchmark or CUDA compatibility claim. The accepted small-sample wake
accuracy is not a guarantee of perfect recognition in every room or voice.

### Opt-in full CLI/native regression

`TestSherpaNativeCLIWorkflow` builds a fresh CGO-free CLI/daemon, starts a private
daemon with private HOME/XDG directories, Unix socket and SQLite database, and
runs the real `voice stream --save --trigger GINKGO`, `voice watch`, and
`voice stop`/`voice cancel` commands. It substitutes only a paced file-PCM recorder
for the microphone; helper, KWS, VAD and Parakeet inference are real. It does not
install anything, touch a system service or use your real notes.

Supply a private TOML/JSON configuration containing installed helper/model paths
and a private JSON case list; neither audio nor expected dictation belongs in Git:

```json
[
  {"name":"dictation", "wav":"/private/full-recording.wav", "want":["expected dictation substring"], "save":true, "action":"stop"},
  {"name":"caption-only", "wav":"/private/full-recording.wav", "want":[], "save":false, "action":"stop"},
  {"name":"negative", "wav":"/private/ambient-or-control-only.wav", "want":[], "save":true, "action":"stop"},
  {"name":"cancel-selected", "wav":"/private/full-recording.wav", "want":[], "save":true, "action":"cancel", "stop_at_ms":8000, "require_selection":true}
]
```

Use a cut point appropriate to your own fixture for stop/cancel cases: the
recorder pauses there with the pipe still open, and `require_selection` verifies
that a selected recording is still active. A stop-selected case uses the same
mechanism with `action: "stop"` and the expected partial dictation. Without a cut
point, the entire original recording plus four seconds of silence is replayed in
real time. Do not use an isolated excerpt when earlier audio affects wake state.
Add a full mixed recording to test that standalone wakes expire before later
ambient speech and that known missed wakes cannot inherit old save authority.

```sh
GINKGO_VOICE_E2E_CONFIG=/private/native-config.toml \
GINKGO_VOICE_E2E_CASES=/private/cases.json \
GINKGO_VOICE_E2E_ARTIFACTS=/private/native-results \
GINKGO_VOICE_E2E_HELPER_COMMIT=the-helper-source-commit \
go test ./tests -run '^TestSherpaNativeCLIWorkflow$' -count=1 -timeout 30m -v
```

Python, microphone access and downloads are not required. Models must already be
installed, Go dependencies cached, and `/tmp/opencode` available for short private
socket paths. The test forces CPU and a three-second post-wake wait and imports
only the supplied `voice` settings, never storage/remotes. For every case it
checks actual SQLite-backed exported notes, exact selected-final `OriginalText`,
trusted engine/model/language provenance, unique saved session IDs, expected note
counts, stop/cancel acknowledgement and recorder-process exit. Expected text is
not printed in test output. `GINKGO_VOICE_E2E_ARTIFACTS` is optional; when supplied,
it **retains private note bodies, events, database and logs** in mode-0700 case
directories outside Git. Without it, temporary files are cleaned up automatically.
Each case also records actual CLI/helper SHA-256 hashes, the checked-out source
commit and dirty-state flag. The optional helper-commit variable records separate
native build provenance; hashes identify the executables actually exercised.
