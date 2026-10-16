# Optional AI note POC

**Status: implemented and smoke-tested; provisional POC, not a general AI assistant.**
The daemon owns provider calls and note writes. AI is disabled by default. Existing
note commands continue to work without an AI provider.

## Configure OpenCode

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
does not protect that separate provider request. The OpenCode adapter uses isolated,
tool-free generation rather than a normal coding-agent session.

## No-paid black-box smoke test

The isolated fake-provider test uses only a temporary config, database, socket, and
owned daemon; it makes no provider/model call and does not touch the user config or
installed service:

```sh
go test ./...
python3 scripts/smoke-ai-poc.py
```

The script builds the binary in its private `/tmp/opencode` directory. It covers
create/expand, CAS, original display, invalid/disabled raw fallback, and disconnect
cancellation without a late note. See the
[`OpenCode adapter notes`](../internal/ai/README.md) for optional no-inference
compatibility tests.

## Not wired in this POC

An internal `whisper-cli` WAV transcription adapter exists, but there is no user-facing
voice CLI or transcript-to-note workflow yet. Microphone capture, STT residency/model
catalog or downloads, wake/VAD, TTS, AI search/summaries, and Codex/Claude adapters
remain deferred. Do not infer these capabilities from this document or the broader
proposal in `PLAN.md`.
