# Voice

## Quick setup

Voice capture requires Linux, PipeWire's `pw-record`, and the native GinkGo voice
bundle (build instructions below). Keep its `bin/` and `lib/` together. Run the
bundled CLI, or put its `bin/` on `PATH`, then provision the standard models:

```sh
ginkgo-cli voice setup
```

Setup downloads missing models (about 2.5 GB), verifies their SHA-256 checksums,
and initializes the native helper to check compatibility and the compiled wake
word. It never opens the microphone. Re-running it reuses verified files;
`ginkgo-cli voice setup --check` verifies an existing installation offline.

The normal [configuration](config.md) needs only:

```toml
[voice]
enabled = true
mode = "raw"                 # or "ai", with a configured AI provider
```

Restart the daemon with the same config, then start capture explicitly:

```sh
ginkgo-cli voice stream --save
ginkgo-cli voice status --output json
ginkgo-cli voice stop         # finish and save selected speech
# or: ginkgo-cli voice cancel # discard pending speech
```

Say **“Hey Ginkgo”**, then dictate immediately or after a short pause. Wake-only
speech saves nothing. Omit `--save` for captions only; `voice watch --output json`
shows live events. AI mode also needs [AI setup](ai.md); AI failure preserves raw
input. Setup does not edit configuration or enable capture.

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

<details>
<summary>Legacy Whisper workflows: WAV transcription, dictation and captions</summary>

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

</details>

## Native bundle and model profile

The experimental Sherpa backend requires GinkGo's native keyword-clock adapter
and a versioned model profile. The supported bundle uses CPU ONNX Runtime. Voice
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

After installing the bundle, run `voice setup`. The standard profile
`sherpa-1.13.8-parakeet-v2-hey-ginkgo-v1` pins these assets:

- Silero VAD ONNX model for 16 kHz audio and 512-sample windows.
- English Parakeet TDT 0.6B v2 FP32 encoder, decoder, joiner, tokens, and any external weight sidecars.
- Frozen `sherpa-onnx-kws-zipformer-zh-en-3M-2025-12-20` FP32 epoch13/avg2/chunk16/left64
  encoder, decoder, joiner, tokens, and compiled keyword file. The supported phrase
  “Hey Ginkgo” uses `HH EY1 G IH1 NG K OW1 @GINKGO`.

The complete paths, download revisions, sizes and SHA-256 hashes are in
[`voiceassets/profile.go`](../internal/voiceassets/profile.go). Parakeet uses
publisher revision `86891485dd8ad7cb28cb1aade45c3e23d0197c30` (CC-BY-4.0);
the KWS export is Apache-2.0 and Silero is MIT. Setup also provisions
`encoder.weights` beside `encoder.onnx` and the compiled “Hey Ginkgo” path.
Native startup still checks the audited KWS hashes, keyword pronunciation,
selected label, runtime identity and owned keyword-clock ABI. It does not accept
arbitrary wake phrases or substitute a different/quantized KWS model.

</details>

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

Optional [screenshot context](image-context.md) captures a window, focused monitor,
or all monitors on each accepted Sherpa AI wake. Automatic capture requires known
model/adapter image support; explicit `context attach` and `context capture`
commands also work with automatic capture disabled. Hyprland is the initial backend.

<details>
<summary>Advanced settings</summary>

The defaults are suitable for normal use. All existing settings remain optional
overrides, registered in the **Advanced settings** block of
[`ConfigOption`](../internal/config/config.go). `config generate` omits these
settings, and `config generate --update` preserves existing overrides.
When adopting the standard setup from an older generated config, remove its
explicit stream/event disable switches and Whisper backend value if you want
the new defaults; setup deliberately retains those choices.

- `voice.stream.enabled` and `voice.events.enabled` follow `voice.enabled` when
  omitted; explicit `false` still disables each feature. `voice.stream.backend`
  defaults to `auto`: Sherpa normally, Whisper for existing configurations with
  `voice.stream.allow_local_http = true`. Explicit backends retain their meaning.
- All `voice.sherpa.*` settings remain supported: helper, provider/CPU fallback,
  compiled label/file, Silero path, Parakeet and KWS encoder/decoder/joiner/tokens,
  recording bounds, startup/shutdown/trigger timeouts, cooldown and pre-roll.
- All `voice.capture.*`, `voice.stt.*`, `voice.stream.*` and
  `voice.trigger.phrase` overrides remain supported, including executables,
  capture target, language, device, threads and timing. Whisper still requires a
  compatible installed GGML model and explicit HTTP consent for live captions.

Nonempty asset paths win over discovery, including environment overrides such
as `GINKGO_VOICE_SHERPA_PARAKEET_ENCODER`. Explicit executable paths never fall
back to another binary. Bare executable names are searched on `PATH`, beside
the resolved GinkGo binary, in `~/.local/bin`, `/usr/local/bin`, then `/usr/bin`.

Models are discovered using the relative layout in the profile, in this order:

1. `data_dir/voice/sherpa-1.13.8-parakeet-v2-hey-ginkgo-v1` (setup destination).
2. `data_dir/models`.
3. The resolved helper bundle's `share/ginkgo/voice/<profile>` and `models/`.
4. `$XDG_DATA_DIRS/ginkgo/voice/<profile>` (default `/usr/local/share`, `/usr/share`).

Custom Parakeet/VAD files are checked for readability and native compatibility;
custom KWS files must still match the audited hashes and compiled pronunciation.
Keep external weight sidecars beside custom encoders. Setup never overwrites
custom or corrupt files; inspect the reported path before retrying. It downloads
only missing standard assets, and reports missing binaries with installation
guidance. Discovery and native probing happen only for setup or an enabled
capture request, never for ordinary config loading or status.

</details>

## Limits and troubleshooting

| Problem | Check |
| --- | --- |
| Voice disabled or old settings | Check config path and daemon release; restart daemon after edits. |
| WAV rejected | PCM16 mono 16 kHz RIFF/WAVE, regular file, within 32 MiB/five-minute defaults. |
| No speech or noise triggers | Check PipeWire target; tune `threshold` and `silence`. Noise can pass the RMS energy threshold. Preview before continuous saving. |
| Busy or stop still active | Inspect `voice status`; stop drains, disable/cancel discards. CLI timeout does not necessarily cancel daemon work. |
| Whisper captions but no note | Add `--save` and a nonempty literal leading `--trigger`; only final text can match. |
| Sherpa starts but no capture | Run `voice setup --check`; wait for model readiness. Stream admission can precede initialization. |
| No Sherpa note | Confirm `--save`, supported compiled label, actual wake and selected speech. A missed/uncertain wake intentionally saves nothing. |
| Raw note in AI mode | Check `ai.enabled`, provider configuration, and returned fallback metadata. |
| Device loss or stalled capture | Repair the PipeWire source and start again. Automatic reconnect/suspend recovery is not provided. |

Whisper capture requires at least 150 ms above the RMS threshold. Defaults are
1.5 seconds of ending silence, two minutes per utterance, and 30 seconds to start
a one-shot recording. Sherpa uses `voice.sherpa.max_recording` for the whole
selection, including wake, pre-roll, pause, and dictation.

Saved transcripts follow the [note storage and export rules](usage.md#export-import-and-originals).
