# Voice

Voice capture requires Linux, PipeWire, and separately installed speech tools and
models. Merge the relevant settings into your [config](config.md), restart the
daemon, then start capture with a command.

## Choose a workflow

| Goal | Command | Engine and saving |
| --- | --- | --- |
| Transcribe a WAV | `voice transcribe FILE` | On-demand Whisper; preview unless `--save` |
| Dictate one utterance | `voice listen` (or `voice record`) | PipeWire plus on-demand Whisper; preview unless `--save` |
| Save every completed utterance | `voice enable` | Continuous PipeWire/Whisper; each nonempty utterance is saved |
| Live captions; optional prefix-gated note | `voice stream` with Whisper | Resident `whisper-server`; raw notes only, gated by final transcript prefix |
| Acoustic wake, then dictate | `voice stream` with Sherpa | Native helper, Silero and Parakeet; optional AI for saved notes |

`enable` and `listen` always use Whisper. `stop` finishes active speech and drains
accepted work; `disable` or `cancel` discards pending work and cancels inference.
Only one microphone job runs at a time. A daemon restart leaves capture idle.

## Whisper setup: WAV and dictation

Install `whisper-cli` and a compatible GGML model, such as `ggml-base.en.bin` for
English. Give the daemon access to both:

```toml
[voice]
enabled = true
mode = "raw"

[voice.stt]
executable = "/absolute/path/to/whisper-cli"
model = "/absolute/path/to/ggml-base.en.bin"
language = "en"
device = "cpu"
threads = 4
timeout = "5m"
residency = "on_demand"

[voice.capture]
executable = "pw-record"
target = ""                    # default PipeWire source
silence = "1.5s"
max_duration = "2m"
start_timeout = "30s"
threshold = 0.02
```

Install `pw-record` for microphone use. `voice enable` starts a runtime job;
the `[voice].enabled` setting must already be true.

Try a single utterance first:

```sh
ginkgo-cli voice listen --silence 2s
ginkgo-cli voice listen --silence 2s --save --mode raw
ginkgo-cli voice enable --namespace work --silence 2s --mode raw
ginkgo-cli voice status --output json
ginkgo-cli voice stop       # finish speech and drain
# or: ginkgo-cli voice disable   # discard pending work
```

Continuous capture survives the initiating CLI's exit. `disable` and `cancel`
stop further writes before returning; previously committed notes remain. Add
`--mode ai` to a saving command to use a [configured AI provider](ai.md).

For file transcription, provide a regular PCM16, mono, 16 kHz RIFF/WAVE file on the daemon's filesystem:

```sh
ginkgo-cli voice transcribe /absolute/private/recording.wav
ginkgo-cli voice transcribe /absolute/private/recording.wav --save --mode raw
```

Use `pw-record --container wav --rate 16000 --channels 1 --format s16 FILE` to
record this format. File limits default to 32 MiB and five minutes. Temporary
microphone WAVs are removed after processing.

## Whisper live captions (optional)

This mode runs an installed `whisper-server` and its compatible model. It opens a local unauthenticated HTTP endpoint, so enable it only when local processes/users are trusted. Merge these settings with the previous configuration; retain `voice.stt` for legacy commands.

```toml
[voice.events]
enabled = true

[voice.stream]
enabled = true
backend = "whisper"
executable = "/absolute/path/to/whisper-server"
allow_local_http = true
update_interval = "500ms"
window = "5s"

[voice.trigger]
phrase = "ginkgo take a note"
```

Start and observe in separate terminals:

```sh
ginkgo-cli voice stream
ginkgo-cli voice watch --output json
```

To save a raw note when the final transcript begins with the trigger phrase:

```sh
ginkgo-cli voice stream --save --trigger "ginkgo take a note"
```

Whisper checks the complete prefix in final text. Partials cannot start a note,
and Whisper streams support only raw mode.

`voice watch` subscribes without starting recording. Subscribers receive live
transcript text even when saving is off; avoid logging it unintentionally. The
[event protocol](voice-protocol.md#public-events-socket-ndjson-v1) covers integrations.

## Sherpa acoustic wake

The experimental Sherpa backend requires GinkGo's native keyword-clock adapter
and a fixed model profile. The supported bundle uses CPU ONNX Runtime. Voice
tools and models run with the daemon's privileges; use trusted sources.

<details>
<summary>Build and provision the native backend</summary>

### Native setup

The main CLI/daemon stays CGO-free. Build the native helper/bundle from the pinned Sherpa source and matching build using a fresh output path outside the source tree. Dependencies must already be installed/cached; the build does not fetch them:

```sh
make voice-bundle \
  SHERPA_SOURCE="/trusted/sherpa/source" \
  SHERPA_BUILD="/trusted/sherpa/build" \
  BUNDLE_OUTPUT="/absolute/fresh/ginkgo-bundle"
```

Use Sherpa-ONNX v1.13.8 source revision
`11afbd009a7f8c08f4bcf2fc1b265d0df4670fbf` and CPU ONNX Runtime 1.28.2. The build
also requires the exact core/runtime artifact hashes in
[`nativebridge/CMakeLists.txt`](../internal/sherpavoice/nativebridge/CMakeLists.txt);
rebuilding the same versions with another toolchain can fail this check.

Required tools are Go, a CGO-enabled C/C++ toolchain, CMake, Git, binutils, and
Linux `ldd`. Go modules must be cached (`GOPROXY=off`, `GOTOOLCHAIN=local`).
`SHERPA_PREFIX` and `ORT_PREFIX` can override installed headers and libraries;
the pinned source/build pair is still required. An ORT prefix needs `VERSION_NUMBER`,
`LICENSE`, and `ThirdPartyNotices.txt` alongside `include/` and `lib/`.

Keep `bin/` and `lib/` together when moving the output. Verify it with
`sha256sum -c SHA256SUMS` from the bundle directory. The target `make voice`
builds the helper alone using `VOICE_BUNDLE_OUTPUT` for its destination.

Provision these models separately from trusted sources and check their licenses, versions, and checksums:

- Silero VAD ONNX model.
- English Parakeet TDT 0.6B v2 FP32 encoder, decoder, joiner, tokens, and any external weight sidecars.
- Frozen `sherpa-onnx-kws-zipformer-zh-en-3M-2025-12-20` FP32 epoch13/avg2/chunk16/left64
  encoder, decoder, joiner, tokens, and compiled keyword file. The supported phrase
  “Hey Ginkgo” uses `HH EY1 G IH1 NG K OW1 @GINKGO`.

Keep external weight sidecars beside their graphs. Native startup checks the KWS
profile against [`native_profile.go`](../internal/sherpavoice/native_profile.go).
Other keyword models or tokenizations require changes to that profile.

</details>

Merge config using the actual installed paths (the filenames here are examples):

```toml
[voice]
enabled = true
mode = "raw"

[voice.events]
enabled = true

[voice.stream]
enabled = true
backend = "sherpa"

[voice.capture]
executable = "/absolute/path/to/pw-record"
target = ""
silence = "1.5s"

[voice.sherpa]
executable = "/absolute/path/to/ginkgo-bundle/bin/ginkgo-voice"
provider = "cpu"
cpu_fallback = true
keyword = "GINKGO"
max_recording = "60s"
keywords_file = "/models/kws-hey-ginkgo.txt"
silero_model = "/models/silero/silero_vad.onnx"
startup_timeout = "2m"
shutdown_timeout = "30s"
trigger_timeout = "3s"
cooldown = "1s"
pre_roll = "500ms"

[voice.sherpa.parakeet]
encoder = "/models/parakeet-v2-fp32/encoder.onnx"
decoder = "/models/parakeet-v2-fp32/decoder.onnx"
joiner = "/models/parakeet-v2-fp32/joiner.onnx"
tokens = "/models/parakeet-v2-fp32/tokens.txt"

[voice.sherpa.kws]
encoder = "/models/zh-en3M/encoder.onnx"
decoder = "/models/zh-en3M/decoder.onnx"
joiner = "/models/zh-en3M/joiner.onnx"
tokens = "/models/zh-en3M/tokens.txt"
```

Stop the existing daemon and start the bundled `bin/ginkgod`. Then run:

```sh
ginkgo-cli voice stream --save --trigger GINKGO
ginkgo-cli voice status --output json
ginkgo-cli voice stop       # finalize selected note
# or: ginkgo-cli voice cancel
```

`--trigger` selects a compiled label. Say “Hey Ginkgo”, then dictate in the same
breath or shortly after. Wake-only speech saves nothing; missed or rejected wakes
also produce no note. The original retains the full selected transcript, including
the control phrase. Each run ends after eight hours or 4096 utterances/selections;
restart it to continue.

### Optional Sherpa AI processing

Sherpa saved notes use `voice.mode` (default `raw`); `--mode ai` overrides it for one run. AI also requires `[ai].enabled = true` and a configured provider. An explicit provider override requires saved AI mode:

```sh
ginkgo-cli voice stream --save --trigger GINKGO --mode ai --provider opencode
```

AI processes each selected transcript as a new note request and preserves it as
the original. See [AI fallbacks and privacy](ai.md#fallbacks-and-errors).

## Limits and troubleshooting

| Problem | Check |
| --- | --- |
| Voice disabled or old settings | Check config path and daemon release; restart daemon after edits. |
| WAV rejected | PCM16 mono 16 kHz RIFF/WAVE, regular file, within 32 MiB/five-minute defaults. |
| No speech or noise triggers | Check PipeWire target; tune `threshold` and `silence`. Noise can pass the RMS energy threshold. Preview before continuous saving. |
| Busy or stop still active | Inspect `voice status`; stop drains, disable/cancel discards. CLI timeout does not necessarily cancel daemon work. |
| Whisper captions but no note | Add `--save` and a nonempty literal leading `--trigger`; only final text can match. |
| Sherpa starts but no capture | Wait for model readiness; check helper/library paths and model files. Stream admission can precede initialization. |
| No Sherpa note | Confirm `--save`, supported compiled label, actual wake and selected speech. A missed/uncertain wake intentionally saves nothing. |
| Raw note in AI mode | Check `ai.enabled`, provider configuration, and returned fallback metadata. |
| Device loss or stalled capture | Repair the PipeWire source and start again. Automatic reconnect/suspend recovery is not provided. |

Whisper capture requires at least 150 ms above the RMS threshold. Defaults are
1.5 seconds of ending silence, two minutes per utterance, and 30 seconds to start
a one-shot recording. Sherpa uses `voice.sherpa.max_recording` for the whole
selection, including wake, pre-roll, pause, and dictation.

Saved transcripts follow the [note storage and export rules](usage.md#export-import-and-originals).
