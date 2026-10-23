# Antigravity CLI (`agy`)

GinkGo can use Antigravity's native Google-account OAuth login and Gemini 3.8
Flash through a persistent `stream-json` process. Install `agy` using Google's
[installation instructions](https://antigravity.google/docs/install/), then run
it interactively and sign in with your Google account:

```sh
agy
agy models
```

Use the same user, `HOME`, and keyring session as the GinkGo daemon. Authentication
and token refresh belong to `agy`; GinkGo does not copy tokens or require an API
key. This adapter does not forward Gemini/Google API-key environment selectors.
Choose Google-account authentication in the CLI rather than API-key mode.

## Configuration

Merge this into your GinkGo configuration and restart the daemon:

```toml
[ai]
enabled = true
provider = "agy"
timeout = "120s"

[ai.providers.agy]
type = "antigravity"          # "agy" is also accepted
executable = "/absolute/path/to/agy"
model = "gemini-3.8-flash-medium"
# variant = "low"            # optional native --effort override
# temp_dir = "/private/tmp"  # parent directory must already exist
# auth_home = "/absolute/path/to/dedicated-home"
```

`executable` defaults to `agy` on the daemon's `PATH`. The model defaults to
**`gemini-3.8-flash-medium`**, independently of Antigravity's interactive model
selection. `agy models` lists the model slugs available to your account. GinkGo
does not silently substitute another model when the selected one is unavailable.

`auth_home` is optional. For a dedicated profile, first create a private directory
and run `HOME=/absolute/path/to/dedicated-home /absolute/path/to/agy` interactively
to sign in. Set `auth_home` to that directory, not to its `.gemini` subdirectory.
Native keyring access still depends on the daemon's desktop/session environment.

```sh
ginkgo-cli ai note --provider agy 'Remember to review the project milestones'
ginkgo-cli ai converse --provider agy 'Find notes about project milestones'
```

Note creation, expansion, search, and reading use GinkGo's existing application
services. The integration adds no provider-level command or note-tool executor.

## Persistent transport and streaming

Each owned process runs with:

```text
agy --input-format stream-json --output-format stream-json \
    --model gemini-3.8-flash-medium --disable-slash-commands
```

GinkGo writes one `{"event":"user","message":{"content":"..."}}` line per
turn, keeps stdin open, and waits for that turn's `result` event. It consumes
`agent_response.text_delta` fragments as they arrive. `Provider.Complete` remains
unchanged; the optional `StreamingProvider.CompleteStream` interface exposes
provisional text deltas and survives registry wrapping. Callbacks run
synchronously and must return promptly. A callback error cancels the turn.
Existing CLI/IPC commands still return their final validated result; they do not
print provisional note JSON.

A provider owns at most one process and serializes requests with cancellable
queueing. Notes reuse a conversation within their namespace; conversational
intent requests also include the client/session scope. A changed scope, model,
effort, system prompt, or output schema starts a fresh conversation. Unscoped
direct `Request` calls are disposable. On the next request, sessions are rotated
after 32 successful turns or 30 minutes of inactivity. Daemon shutdown closes
owned processes and removes their temporary workspaces.

Antigravity has no separate system-prompt flag in this protocol. GinkGo includes
the instructions and the complete current context in each input message.
Output schemas are supplied as prompt instructions, with GinkGo's strict note
and intent validation remaining authoritative. Native `--json-schema` is not
used: it requires executing the `finish` tool, which conflicts with the deny-all
tool policy.

Antigravity stores conversations in its native profile, including submitted
text. Removing a GinkGo temporary workspace does **not** delete native history.
Warm conversations retain earlier turns within their scope until rotation.

## Scoped tool permissions

Every private workspace contains `.agents/hooks.json` with a `PreToolUse` hook
matching `*`. It returns:

```json
{"decision":"deny","reason":"Ginkgo generation-only tool policy"}
```

This blocks native model-callable tools, including command execution, file
access, MCP, and subagents, without changing global permissions. Slash-command
expansion is disabled, and GinkGo never passes `--dangerously-skip-permissions`.
Unexpected completed tool calls are treated as an isolation failure. This is a
model-tool policy, not an OS sandbox: native global startup hooks, plugins, and
profile customizations remain native-owned. Use a dedicated login profile if
you need to keep those separate from your ordinary Antigravity sessions.

## Images

**The current `agy` streaming input protocol supports text blocks only.** The
installed CLI was tested with a synthetic PNG image block and returned:

```text
stream input content block type "image" is not supported (only "text")
```

It ended the session with status `ERROR` and exit code 1. Consequently this
adapter reports image input as `unsupported`, even when `image_input =
"supported"` is configured or the selected model itself supports vision.
GinkGo retains explicit image attachments locally, reports that they were skipped
for inference, and continues with the text note. Automatic screenshot admission
remains disabled for this adapter. No image is converted into a filename or a
text/base64 substitute and claimed as visual input.

## Failures and recovery

- **Login/refresh failure:** classified as authentication failure. Run `agy`
  interactively using the daemon's profile and sign in again, then retry.
- **Quota/rate exhaustion:** classified as rate-limited. Wait for the native
  quota reset; GinkGo neither retries the failed prompt nor changes accounts or
  models. Native credit-spending preferences remain controlled in `agy`.
- **Idle process crash:** the next request starts `agy --conversation ID` for
  the last successful conversation. Only the new request is sent.
- **Active crash, timeout, malformed output, callback failure, or non-success
  result:** the process is reaped and its session retired. The next explicit
  request starts fresh. An ambiguously submitted turn is never replayed. A failed
  resume is likewise retired, allowing a fresh session on the following request.
- **Daemon restart:** starts fresh; GinkGo does not persist native resume IDs.

Input, output, stderr, and request duration are bounded. Cancellation kills and
joins the owned process group. Raw native errors are classified rather than
copied into user-facing diagnostics. Existing note fallback preserves raw input;
strict conversational requests still report failure instead of creating a raw
note.

## Verification

The implementation follows Google's [headless protocol](https://antigravity.google/docs/cli/headless/)
and [workspace hooks](https://antigravity.google/docs/hooks/) contracts. On
2026-10-23, local native checks confirmed Google-authenticated two-turn reuse,
Gemini 3.8 Flash model availability, streaming deltas, image-block rejection,
and hook denial of a synthetic `run_command` call. Unlike the other pinned
native adapters, this adapter uses the documented protocol rather than a fixed
release-number gate; incompatible framing fails the request.

Run the deterministic process-fixture tests with:

```sh
go test ./internal/ai -run Antigravity
```

To verify native image-block rejection without submitting an inference turn:

```sh
GINKGO_AGY_PROTOCOL_SMOKE=1 GINKGO_AGY_EXECUTABLE=/absolute/path/to/agy \
  go test ./internal/ai -run '^TestAntigravityNativeImageRejection$' -count=1 -v
```

For two synthetic authenticated turns against your installed CLI (uses model
quota), explicitly opt in:

```sh
GINKGO_AGY_LIVE_SMOKE=1 GINKGO_AGY_EXECUTABLE=/absolute/path/to/agy \
  go test ./internal/ai -run '^TestAntigravityLiveStreamSmoke$' -count=1 -v
```
