# Optional voice and AI note POC

**Status: provisional POC, not a general AI assistant.** File transcription,
PipeWire microphone listening, and AI note workflows are implemented; isolated
fake-device/engine/provider tests cover their behavior. A real local file-to-note
smoke test also passed with whisper.cpp
v1.7.6, the English-only `base.en` model, and the public upstream `jfk.wav` sample.
The verified local runtime and model are installed under
`$HOME/.cache/ginkgo/voice-poc/`; see the repeatable real-audio smoke command below.

The daemon owns transcription, provider calls, and note writes. Voice and AI are
independently disabled by default. Existing note commands need neither dependency.

## Start with raw local voice transcription

Prerequisites: Linux with `/proc`, a trusted installed whisper.cpp **`whisper-cli`**
executable, and its compatible installed `ggml-base.en.bin` English model
(approximately **142 MiB**). This is not Python's `whisper` CLI. Models live in a
user-managed local cache; GinkGo neither downloads them nor manages that cache.
Replace both absolute-path placeholders below with the final installed locations.
The daemon must be able to read the WAV/model and execute whisper-cli.

Merge these tables into the existing GinkGo TOML config; do not duplicate tables:

```toml
[voice]
enabled = true
mode = "raw"

[voice.stt]
executable = "/absolute/path/to/whisper-cli"
model = "/absolute/path/to/local-cache/ggml-base.en.bin"
language = "en"
device = "cpu"
threads = 4
timeout = "5m"
residency = "on_demand"

[ai]
enabled = false
```

`language = "en"` is appropriate for the English-only `base.en` model. A compatible
multilingual model may instead use a language code or `"auto"`. Only
`residency = "on_demand"` and `device = "cpu"` or `"auto"` are supported; `auto`
lets the installed engine choose CPU/GPU and does not guarantee GPU availability.
See the [adapter compatibility and resource limits](../internal/voice/README.md).

Build matching CLI/daemon binaries and **restart the daemon after changing config**
using your normal service workflow (for the installed user service:
`systemctl --user restart ginkgo.service`). `make build` builds the binary but does
not update a running daemon. These instructions do not change your config or
restart your service automatically.

### Listen directly from the microphone

With the voice configuration above and PipeWire's `pw-record` installed:

```sh
ginkgo-cli voice listen --silence 2s                  # transcript only
ginkgo-cli voice listen --silence 2s --save           # one saved utterance
ginkgo-cli voice enable --silence 2s --mode raw       # continuously save utterances
ginkgo-cli voice status --output json
ginkgo-cli voice disable                            # discard pending work, release mic
```

`voice stop` finalizes current speech and drains accepted utterances instead of
discarding them. See [listening configuration and lifecycle](voice-listening.md).
Continuous listening is explicit and lasts until disabled or the daemon exits.

### Alternatively, record a compatible WAV externally

Use the installed PipeWire recorder directly, then press **Ctrl-C** to finish:

```sh
pw-record --container wav --rate 16000 --channels 1 --format s16 /tmp/recording.wav
```

This external recording path is useful for reusable file fixtures. Audio
must be a regular RIFF/WAVE file: PCM signed 16-bit, 16000 Hz, mono. Other audio
formats are rejected rather than converted. Default limits are 32 MiB and five
minutes of audio. The file path refers to the daemon's local filesystem; no audio
upload is sent through IPC. Recordings can contain private information; remove
your test recording when you no longer need it.

### Test raw first, then AI mode

Before saving voice provenance, [upgrade every synced writer](provenance.md): older
binaries can discard originals when they rewrite a note.

```sh
make build
# Transcribe only: no note is written without --save.
./build/ginkgo-cli voice transcribe /tmp/recording.wav --output json

# First real test: save the exact transcript with AI disabled.
./build/ginkgo-cli voice transcribe /tmp/recording.wav --save --mode raw --output json

# Optional second test: with AI still disabled, save raw and report fallback_reason.
./build/ginkgo-cli voice transcribe /tmp/recording.wav --save --mode ai --new --output json
```

Each `--save` command creates its own note for nonempty speech; running both saves
two notes. JSON includes `transcript` (`text`, optional `language`, `engine`, `model`)
and, when saved, `entry`, `action`, and optional `fallback_reason`. Empty or
whitespace-only transcripts create **no note**, even with `--save`. Copy the saved
`entry.id` to inspect its body and separately preserved voice original:

```sh
./build/ginkgo-cli note show NOTE_ID
./build/ginkgo-cli note show NOTE_ID --original
```

Raw mode never calls an AI provider. AI mode uses the same transcript-to-note
workflow as `ai note`: until AI is enabled and its provider is configured, it saves
a new raw note with a fallback reason. After configuring OpenCode below and
restarting the daemon, use `--save --mode ai --provider opencode --new` for a new
AI-organized note, or `--note NOTE_ID` instead of `--new` for explicit expansion.
AI failure also falls back to raw; cancellation does not save a fallback note.

Other supported overrides: `--namespace`, `--title`, repeated `--tag`, installed
model path `--model`, and `--language`. `--new`/`--note` require `--save --mode ai`
and are mutually exclusive. `--timeout` bounds the whole request (default eight
minutes); the daemon's STT timeout independently bounds transcription and its
serialized queue wait. STT is on demand: each request starts and releases an
engine process, rather than retaining a model between requests.

## Configure OpenCode for AI notes

Add or merge this into the GinkGo TOML config. Replace the executable and model with
your installed OpenCode path and selected provider/model. Never put API keys in this
file, prompts, or command arguments.

```toml
[ai]
enabled = true
provider = "opencode"
timeout = "120s"

[ai.notes]
context_limit = 8

[ai.providers.opencode]
type = "opencode"
executable = "/path/to/opencode"
model = "provider/your-model"
ephemeral_auth = true
```

For OpenCode **2.0.19**, `ephemeral_auth = true` selects isolated API-key mode; provide
the appropriate provider API-key environment variable to the daemon. Do not copy
credentials into GinkGo config. For stable OpenCode **>=2.0.25**, omit
`ephemeral_auth` (or set it false) to use ordinary saved provider auth. This version
compatibility does not validate a user's real login or model access. See
[`internal/ai/README.md`](../internal/ai/README.md) for provider isolation details.

### Optional session-generation adapter

For stable OpenCode **>=2.0.25**, configure a separate provider to use the supported
session generation-only API:

```toml
[ai.providers.opencode-session]
type = "opencode-session"
executable = "/absolute/path/to/opencode"
model = "provider/your-model"
# ephemeral_auth = true  # optional disposable auth profile, API-key env mode
```

Select it with `--provider opencode-session` or `ai.provider`. It uses a private
server and creates/deletes only its own temporary session. In saved-auth mode,
that session shares the ordinary auth database; failed cleanup or a process crash
can leave a `GinkGo ephemeral generation` session behind. Existing sessions are
not reused. Tools, custom hooks/plugins, MCP, skills and discovered instructions
are disabled; this is not a coding-agent `opencode run` invocation.

A no-inference protocol check passed on **2.0.31** with synthetic saved auth.
Actual model compatibility, including the reported free-tier restriction, remains
unverified. See [session isolation and cleanup details](../internal/ai/README.md#session-generation-adapter).

AI config is loaded by the daemon at startup. Run an AI-capable daemon built from the
same release as the CLI; `make build` builds the CLI only. **Restart the daemon after
changing config** using your normal service workflow. This guide does not restart or
reconfigure a running service for you.

## Create, expand, inspect originals

Build the CLI and pipe exact text to the AI-note command:

```sh
set -euo pipefail
make build
created="$(printf '%s\n' 'A short source note to organize.' \
  | ./build/ginkgo-cli ai note --stdin --provider opencode --new --output json)"
printf '%s\n' "$created"
note_id="$(printf '%s' "$created" | python3 -c 'import json,sys; print(json.load(sys.stdin)["entry"]["id"])')"
printf '%s\n' 'A follow-up detail.' \
  | ./build/ginkgo-cli ai note --stdin --provider opencode --note "$note_id" --output json
./build/ginkgo-cli note show "$note_id"
./build/ginkgo-cli note show "$note_id" --original
```

The JSON response contains `entry`, `action`, and `fallback_reason`; the example
extracts `entry.id` for the explicit expansion.

`--new` and `--note ID` are mutually exclusive. `--note` is explicit, namespace-
scoped expansion using CAS. Expansion is append-only: the model proposes only new
material; GinkGo retains the existing body, tags, and provenance. Auto action can use
a bounded set of recent notes (`ai.notes.context_limit`, default 8); forced `--new`
gets no note context and explicit `--note` gets only its target. This POC does not use
topic FTS retrieval or infer namespaces. Choose the namespace explicitly with
`--namespace` or configure the default namespace.

Original input is stored separately from cleaned body text and is not included in
ordinary rendering or FTS. Use `note show --original` to display it. Provenance storage,
writer compatibility, migration, and sync behavior are documented in
[`provenance.md`](provenance.md). **Upgrade every synced writer before using provenance**:
older binaries may reserialize entries without preserving originals, and there is no
mixed-version capability fence.

## Fallback and cancellation

With AI disabled, unavailable/invalid provider configuration, provider failure/timeout,
invalid draft, or an expansion conflict, the daemon saves the exact input as a new raw
note and returns a fallback reason; a failed expansion leaves its target unchanged.
A caller/request cancellation is different: it cancels provider work and **does not
save a fallback note**. If the caller disconnects or its deadline expires,
preserve/re-submit the input yourself if needed.

Remote providers receive the input and any selected bounded note context. Sync E2EE
does not protect that separate provider request. Both OpenCode adapters use isolated,
tool-free generation rather than a normal coding-agent prompt.

## No-paid black-box smoke test

The isolated fake-provider test uses only a temporary config, database, socket, and
owned daemon; it makes no provider/model call and does not touch the user config or
installed service:

```sh
go test ./...
python3 scripts/smoke-ai-poc.py
python3 scripts/smoke-voice-poc.py
python3 scripts/smoke-voice-listening.py
```

The script builds the binary in its private `/tmp/opencode` directory. It covers
create/expand, CAS, original display, invalid/disabled raw fallback, and disconnect
cancellation without a late note. See the
[`OpenCode adapter notes`](../internal/ai/README.md) for optional no-inference
compatibility tests.

To repeat the real local speech test with the installed POC assets (all notes
remain in disposable storage):

```sh
python3 -B scripts/smoke-voice-poc.py \
  --whisper "$HOME/.cache/ginkgo/voice-poc/bin/whisper-cli" \
  --model "$HOME/.cache/ginkgo/voice-poc/models/ggml-base.en.bin" \
  --audio "$HOME/.cache/ginkgo/voice-poc/samples/jfk.wav"
```

## Deferred in this POC

Wake words, semantic speech detection, TTS, automatic suspend/device recovery,
hooks/visualizers, resident models (`persistent`/`idle_timeout`), model
catalog/download management, AI search/summaries, and Codex/Claude adapters remain
deferred. OpenCode's original stateless path remains available alongside the
session-generation adapter; real model compatibility still needs verification. Microphone
endpointing currently uses RMS energy and can mistake noise for speech. The
public-sample smoke test verifies the pinned CPU file-transcription build, not
microphone quality or compatibility with every whisper.cpp release.
