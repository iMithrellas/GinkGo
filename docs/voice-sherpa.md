# Optional Sherpa voice helper

**Current blocker: native acoustic-wake note recording is unavailable.** The
pinned v1.13.8 KWS reset paths cannot currently prove epoch-relative wake
attribution. Native `--save` initialization therefore fails closed **before
constructors or microphone capture**, including on CPU; fallback does not fix
the clock. Canonical spoken-form validation is also required independently of
keyword aliases. See the [helper safety details](../internal/sherpavoice/README.md).
Caption-only native support remains implemented but unverified on real hardware.
Fake-engine note-saving tests validate the daemon/protocol path, not native
recording support. This feature is **not complete or ready for final approval**.

This optional live-caption backend on Linux is **opt-in**, not a
replacement for existing `voice enable`, `voice listen`, or file transcription.
Those commands keep their Whisper/AI behavior. The default stream backend is
still `whisper` so existing configurations do not change behavior.

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

The source/build pair is required to compile GinkGo's `ginkgo-kws` C ABI shim;
integrate `internal/sherpavoice/nativebridge` before building. The CMake contract
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

Packaging does not launch a daemon or install/restart a service. Set the existing
daemon's helper configuration to the absolute `bin/ginkgo-voice` path only when
you explicitly choose to activate it. Starting `bin/ginkgod` is a separate,
explicit action; do not start another daemon on an already-owned socket. A help
or loader smoke check proves neither ASR model readiness nor native note-saving
safety. Recording stays gated as described above.

Install a trusted `pw-record` and a trusted helper executable yourself. Native
code and models execute locally with the daemon's privileges; do not point these
settings at untrusted binaries. No command installs drivers, libraries, or models,
downloads assets, starts the helper automatically, or enables capture at startup.

Acquire these model assets manually from the Sherpa upstream model documentation:

- English **Parakeet TDT 0.6B v2 FP32**, encoder, decoder, joiner and matching tokens.
  Use FP32 v2 for both CPU and CUDA initially; neither quantized assets nor a
  similarly named v3/model architecture are interchangeable.
- The English Sherpa keyword-spotting transducer's matching encoder, decoder,
  joiner, tokens, and **compiled keywords file**.
- A compatible Silero VAD ONNX model.

Models stay external to the binary set. Parakeet FP32 (~2.5 GB, including the
encoder's external `encoder.weights` data) and Silero require explicit provisioning
permission; filenames alone do not prove completeness. Preserve external
ONNX weight sidecars beside their graph. The frozen KWS model is
`sherpa-onnx-kws-zipformer-zh-en-3M-2025-12-20`, with compiled keyword phones
`HH EY1 G IH1 NG K OW1 @GINKGO`. Its accepted 5/6 wake result is not a promise of
perfect recall and does not waive sample-clock/provenance alignment gates. No
further model tuning or automatic model fetching is part of packaging.

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
export VOICE_KEYWORDS_FILE="/absolute/path/to/frozen-compiled-keywords.txt"
```

Use the existing frozen compiled keyword file, not an invented filename or a new
tokenization run. The example selects FP32 chunk-16 KWS graphs. Its model paths
are illustrative references, not model hash verification; retain/verify the model
provenance separately. Update the helper path after relocating the bundle. Never
replace existing user configuration with the example wholesale.

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
keywords_file = "/models/english-kws/keywords.txt"
silero_model = "/models/silero_vad.onnx"
startup_timeout = "2m"             # each pre-capture initialization attempt, <=5m
shutdown_timeout = "30s"           # selected-note finalization + exit, <=5m
trigger_timeout = "30s"            # selected-session bound, <=5m
cooldown = "1s"                    # 0..60s
pre_roll = "1s"                    # 0..30s

[voice.sherpa.parakeet]
encoder = "/models/parakeet-v2/encoder.onnx"
decoder = "/models/parakeet-v2/decoder.onnx"
joiner = "/models/parakeet-v2/joiner.onnx"
tokens = "/models/parakeet-v2/tokens.txt"

[voice.sherpa.kws]
encoder = "/models/english-kws/encoder.onnx"
decoder = "/models/english-kws/decoder.onnx"
joiner = "/models/english-kws/joiner.onnx"
tokens = "/models/english-kws/tokens.txt"
```

The names above are illustrative, not model identity checks. Preserve actual
bundle filenames. Sherpa does **not** require `voice.stream.allow_local_http`,
`voice.stt.model/device/residency`, or a Whisper executable. The resident helper
uses inherited private NDJSON pipes, not an HTTP server. KWS and VAD run on CPU;
the requested provider is for Parakeet. The capture threshold setting belongs to
legacy capture; Sherpa uses acoustic KWS and Silero VAD instead.

## Run and inspect

```sh
ginkgo-cli voice watch --output json          # read-only; safe to start idle
ginkgo-cli voice stream                      # captions only; NO note recording
ginkgo-cli voice status --output json
ginkgo-cli voice stop
# Future selected-note workflow / fake-engine tests only; native init currently rejects this:
# ginkgo-cli voice stream --save --trigger GINKGO --namespace work
ginkgo-cli voice stop                        # finalize selected note, drain, join
# Or: voice cancel / voice disable           # discard pending work, join
```

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

The following selection behavior is implemented for trusted injected frontends
and tested with fake engines; **the current native backend rejects recording**.
It describes the intended contract, not an available native note-saving workflow.

One helper owns one `pw-record` stream and resident models. Re-decoded rolling
windows supply revisable live previews; this is not decoder/token streaming and
there is no fixed latency promise. Acoustic wake changes `recording_note`
immediately, independently of ASR finalization. The selected session retains the
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

The bridge bounds each epoch to 4096 utterances/selected sessions; explicitly
stop/restart long-running captures before exhausting that bound. Silence alone
does not impose an idle timeout or release resident models. Recording consumes
the same exclusive job slot as legacy stream/listen/enable.

## Verification status

Tests use fake installed helpers over actual pipes, private Unix IPC and temporary
storage: pre-silence captions, immediate wake state, exact selected provenance,
ambient/no-save isolation, provider ready/fallback/retry, pre-ready cancel, EOF and
capture failure, graceful stop, queued-candidate discard, and cancellation joined
with deliberately blocked persistence. Bridge tests cover strict protocol and
process-group cleanup. Existing Whisper tests and voice smoke scripts remain
applicable. **No real microphone, model, GPU, driver, native inference or external
provider has been exercised by these integration checks.** Hardware/model quality,
latency, CPU performance and CUDA compatibility remain unverified locally.
