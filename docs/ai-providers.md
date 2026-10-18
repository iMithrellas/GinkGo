# AI provider contracts and verification

GinkGo owns note semantics; a provider only generates a candidate response.
Typed input and saved voice notes use the same service, image policy, strict
validation, raw fallback, provenance, and attachment pipeline. Native coding
agents are sibling integrations, not wrappers around one another.

## Choosing a backend

Use a named `[ai.providers.NAME]` table and select it with `ai.provider` or
`--provider NAME`. Existing provider names, aliases, and defaults retain their
meaning. In particular, `opencode` and `opencode-session` are the original
text-only generation endpoints, not multimodal agent sessions.

### Preserved adapters

| Type | Transport | Images | Version/authentication |
| --- | --- | --- | --- |
| `opencode` | V2 `experimental.generate` | No | Stable V2 >=2.0.25 for saved native login; 2.0.19 supports explicit ephemeral/API-key mode |
| `opencode-session` | V2 `session.generate` | No | Stable V2 >=2.0.25, including ephemeral mode |
| `openai-compatible` | Non-streaming `/chat/completions` | Model metadata or explicit declaration | Configured compatible endpoint; optional API-key environment variable |
| `cli` | One JSON request/response over stdin/stdout | Explicit wrapper/model declaration | Trusted administrator-provided foreground wrapper |

`cli` with `adapter = "opencode"` retains the legacy `opencode` meaning. Generic
wrappers remain responsible for their own native authentication, isolation, and
generation-only controls; calling an agent with permissive defaults is not a safe
wrapper. See [AI configuration](ai.md) for the original contracts.

### Native support matrix

These adapters currently target Linux and deliberately pin audited releases.
Newer releases are unavailable until their isolation and protocol contracts have
been checked; an unknown release is not silently treated as compatible.

| Type | Audited releases | Native authentication accepted | Vision discovery | Schema generation |
| --- | --- | --- | --- | --- |
| `opencode-native` | 2.0.25, 2.0.31 | Saved provider login without Console/managed configuration, or explicit ephemeral API-key mode | Selected model's input modalities | Prompt plus GinkGo validation |
| `codex` | 0.163.0 | Native ChatGPT/OAuth or stored API-key login in a dedicated profile | Native `model/list` input modalities | Native strict `outputSchema` |
| `claude-code` | 2.1.299 | Native subscription/OAuth login or explicitly configured native API/gateway authentication | Unknown; verified explicit declaration needed | Native `StructuredOutput` formatter only |

All rows use fresh generation tasks, remove model-callable action tools, and
retain GinkGo's strict final validation. Codex and Claude account plans are not
blanket rejection criteria. A native policy that conflicts with the required
tool surface can still make generation unavailable. Native policy startup
effects are a separate trust boundary, described below.

### Native OpenCode: `opencode-native`

Audited releases: **2.0.25 and 2.0.31**, not an open-ended `>=` version promise.
Other releases fail the version gate until their session and isolation contracts
have been verified. The original text-only adapters keep their existing gates.

```toml
[ai]
enabled = true
provider = "visual-opencode"
timeout = "120s"

[ai.providers.visual-opencode]
type = "opencode-native"
executable = "/absolute/path/to/opencode"
model = "provider/your-vision-model"
# variant = "your-supported-variant"
image_input = "auto"
# ephemeral_auth = true       # use API-key environment auth, not saved login
```

The daemon owns a reused private V2 server and serializes requests. Each note
gets a fresh dedicated GinkGo-agent session with a deny-all tool snapshot,
separate system instructions, text, and ordered inline data-URI attachments.
System/schema instructions live in private agent configuration, not durable API
instruction entries. GinkGo awaits configuration reload and verifies the exact
system text before creating the session, then resets it after deleting the
session. A failed reset tears down the runtime rather than reusing stale text.
Literal `{env:...}` and `{file:...}` text in instructions is not interpolated.
The adapter verifies admission, waits for completion, and accepts only a completed
assistant message with the intended agent/model. It interrupts/deletes its own
session on failures and never replays an ambiguously accepted prompt.

The adapter accepts and retains up to 8 original PNG/JPEG images, **8 MiB each**,
12 MiB total, and 32 megapixels per image. Images at or below 4 MiB are submitted
byte-for-byte unchanged. Only larger inputs receive deterministic smaller **AI
copies**: full-resolution lossless PNG recompression first, then high-quality
JPEG for opaque images, then aspect-preserving downscaling if necessary. Alpha
images remain PNG; transparency is not flattened. Originals stored, exported,
and synced by GinkGo remain unchanged.

The tighter per-image bound works around the audited releases' grouped
[`Prompt.Base64` validation regex](https://github.com/anomalyco/opencode/blob/22728d6/packages/schema/src/prompt.ts):
the shipped Bun runtime rejects valid roughly 5.3 MiB image payloads, causing an
empty HTTP 500 during prompt admission before any model request. **4 MiB is a
conservative empirical wire workaround, not an official native API limit.** Both
data URIs and local file URIs encounter the validator. Preflight and completion
independently prepare and check the entire batch locally before any submission.
Work is serial, context-bounded, and has fixed pixel, byte, and attempt limits;
no derived copies are written to disk or hosted externally. Preparation failure
skips the entire batch with an explicit status while text processing continues.

The audited built-in image-config plugin remains enabled with automatic resizing
disabled, preventing unreported further transformations. The encoded batch stays
below native pruning limits; input echoes have a separate bounded allowance and
generated output retains its normal limit. `image_status` distinguishes
`retained originals; included reduced-size copies in AI request` from
`retained originals; reduced-size image submission unconfirmed` and known
non-submission. This reports transport/preparation, not whether the model used
the image. Recompression/downscaling can reduce small-text quality; the full
original remains available for inspection and export.

Model discovery uses V2 model metadata (`capabilities.input`); an omitted,
`default`, or `auto` model resolves the isolated runtime's default, not ambient
project settings. No inference is used for discovery. The native prompt endpoint
has no generic schema field, so GinkGo's prompt and strict proposal validation
remain responsible for the note format.

Saved native provider authentication remains in the normal OpenCode profile.
Saved Console credentials and `/etc/opencode` managed configuration are refused
because they can weaken isolation. `ephemeral_auth = true` instead uses disposable
data/state and inherited provider API-key variables. A saved-auth crash or failed
deletion can leave the owned session and its input in the native database;
cleanup failure is reported and owned IDs are retried before new work. This is
not a promise that saved-auth sessions are never written to disk. An ungraceful
process exit can also leave private temporary configuration behind. The adapter
avoids the upstream instruction-entry API's orphaned-blob retention; it never
modifies historical native database records.

### Native Codex: `codex`

Audited release: **0.163.0**. GinkGo owns a private native app-server over stdio,
reuses it across bounded requests, and creates a fresh ephemeral thread per note.
It never resumes a coding conversation. The selected model's native metadata
drives vision capability and supported reasoning effort; omitted models use the
native discovered default, not an arbitrary model guessed by GinkGo.

Create a **dedicated**, private native authentication directory and sign in to
ChatGPT using Codex itself. This must not be your general-purpose Codex profile:

```sh
install -d -m 700 "$HOME/.local/share/ginkgo-codex-auth"
CODEX_HOME="$HOME/.local/share/ginkgo-codex-auth" /absolute/path/to/codex login
```

This uses native subscription/OAuth authentication; GinkGo does not require an
API key. If you intentionally prefer API billing, use native
`codex login --with-api-key` with the same `CODEX_HOME` instead. Do not copy an
existing token file into the profile. Codex owns credential storage and refresh.

Use the resulting absolute, canonical path in GinkGo configuration:

```toml
[ai.providers.codex]
type = "codex"
executable = "/absolute/path/to/codex"
auth_home = "/home/you/.local/share/ginkgo-codex-auth"
model = "your-vision-model"
# variant = "medium"           # must be advertised for the selected model
image_input = "auto"
```

The profile must be owned by the daemon user, mode 0700, and not a symlink.
GinkGo permits opaque native authentication and audited native runtime artifacts
there, including the exact signed native cloud-policy cache. Codex—not GinkGo—
verifies that cache's signature, account binding, and expiry; GinkGo checks the
delivered effective policy before generation. Ordinary configuration,
instructions, skills, hooks, plugins, rules, and unknown files are refused rather
than deleted.
`/etc/codex` must be absent. Use a separate profile for each configured Codex
provider; one provider owns a profile until it is closed, including across
different registry configuration identities. This ownership check is in-process,
not a cross-process lock: do not use that profile concurrently with another
Codex process or GinkGo daemon.

**Native authentication:** `login status` checks the dedicated profile before
app-server startup. Both saved ChatGPT/OAuth and stored API-key login are
supported. GinkGo does not read, copy, or replay native credentials, reject an
account based on its subscription plan, or fall back to an environment key.
Reauthenticate using Codex itself if the native login expires or is revoked.
The ordinary environment, including proxy and provider override variables, is
not inherited by the isolated runtime.

A sanitized static native model catalog and explicit feature controls remove
action tools, including tools that disabling shell alone would leave enabled.
Private HOME/cwd, empty integration roots, and effective-config validation exclude
ordinary ambient customizations. Codex still owns mandatory native cloud policy
loading; it can perform startup work before GinkGo inspects the effective
configuration. Incompatible enforced requirements fail explicitly rather than
being bypassed. Read-only sandbox and approval denial are additional defenses,
not substitutes for tool removal. Model and provider fallback are off.

PNG/JPEG images retain caller order. Larger batches use private mode-0600
`localImage` files containing the exact supplied bytes so native event echoes do
not exceed the output bound; small batches use inline data URLs. Those temporary
files are removed after the request and the private runtime directory at close.
Original attachments in GinkGo are never resized or replaced. Native ephemeral
threads do not save conversation rollouts, but the authentication profile may
still acquire native metadata databases and logs; ephemeral is not a promise of
zero disk writes or zero provider-side retention.

### Native Claude Code: `claude-code`

Audited release: **2.1.299**. The direct Go adapter uses streaming JSON user
messages with text and base64 PNG/JPEG blocks, not a string-only SDK invocation.

Sign in with Claude Code's native login under the same user/profile used by the
GinkGo daemon. Saved subscription/OAuth login is supported directly; no API key
or custom gateway is required, and GinkGo does not copy credentials between
profiles. Native Claude Code owns token storage and refresh.

```toml
[ai.providers.claude]
type = "claude-code"
executable = "/absolute/path/to/claude"
model = "sonnet"               # alias resolved to an exact model before use
image_input = "supported"      # only after checking the selected model's docs
# variant = "medium"           # supported native effort level
```

Initialize-only control messages resolve aliases/defaults without inference.
Claude's supported-model metadata does not expose vision capability, so `auto`
remains unknown; the explicit declaration above is required for images in this
adapter.

Each request gets a disposable process and no persisted conversation. A private
system-prompt file replaces the normal system prompt. Safe mode, empty settings
sources, explicit hook/memory/skill restrictions, empty strict MCP configuration,
and `--tools ""` suppress ordinary user customizations and remove action tools. Native schema
requests expose only the synthetic `StructuredOutput` formatter; no filesystem,
shell, network, or MCP action tool is enabled. Safe mode retains some native
built-in components, so this is not a claim that every built-in plugin is removed.
Only a successful final result is accepted, and schema requests additionally
require `structured_output`.

**Native authentication and policy:** safe mode is used instead of `--bare`,
which skips saved subscription OAuth/keychain login. Native auth-status metadata
checks login without credential-file parsing; account plan is not an isolation
gate. Native managed settings remain authoritative. In particular, organization
startup hooks can execute despite safe mode and local hook-disable settings.
GinkGo does not bypass that policy or claim these startup effects are absent.
An incompatible managed MCP/tool policy must fail rather than enable action
tools. Policy files must be safely inspectable; unsupported external provider or
credential selectors, mismatched profiles, and unaudited WSL policy inheritance
remain unavailable. These are concrete profile/policy limitations, not
subscription-plan restrictions. Use custom endpoints only with explicitly
configured gateway credentials: saved subscription credentials are not redirected
to a custom endpoint. GinkGo never substitutes API billing or another
authentication profile to make an OAuth request succeed.

Known image limits are tighter than original storage: 5 MiB/image, 8000 pixels
per dimension, a conservative 6 MiB image-batch budget, and a 10,000,000-byte
encoded streaming-input ceiling. Oversized batches are skipped before generation;
their original bytes remain unchanged and text processing continues.

## Shared image capabilities

Image submission requires supported model metadata, a capable transport, and no
configuration restriction. `image_input = "supported"` can fill missing model
metadata; it cannot override a known text-only model or transport. `"auto"`
preserves unknown support, and `"unsupported"` disables visual submission.
Unknown or unsupported images are skipped while typed/dictated text continues
through AI and explicit originals remain attached to the saved note.

Capability discovery does not send note content or run a preliminary classifier.
Supplied eligible images reach the selected provider in the same request as the
note, even if the model follows the prompt and ignores them. Neither attached
text nor prior notes can authorize visual inspection. See [image-context
policy and original storage](image-context.md).

## Execution and privacy boundary

- GinkGo does not approve filesystem, shell, network, MCP, or other agent tools
  to obtain note generation. A read-only sandbox, approval denial, or a prompt
  asking the model not to use tools is not equivalent to removing the tools.
- A backend that cannot enforce the required boundary is unavailable; GinkGo
  never silently changes models, switches providers, or enables unsafe tools.
- For Codex and Claude Code, generation-only describes the model's action-tool
  surface, not a sandbox around the trusted native executable or organization
  policy. Native authentication, managed startup hooks, metadata traffic, routing,
  telemetry, and runtime disk writes may still occur. Do not use an untrusted
  native profile or assume initialization is side-effect-free. Mandatory policy
  is not disabled merely to make a subscription account work.
- Provider schemas are an optional generation aid. GinkGo always validates the
  complete note proposal itself, including namespace, eligible target, tags,
  and append-only expansion rules.
- Credentials remain with native authentication or explicitly configured API-key
  environment variables. GinkGo does not extract subscription OAuth tokens and
  replay them through an unrelated API client.
- Provider-side retention is separate from GinkGo's encrypted sync. Disposable
  local request state is not a promise of zero retention at the model service.

## Verification levels

The default test suite is offline and deterministic: fake executables/protocol
servers test framing, bytes/order, runtime controls, cancellation, bounds,
failures, and final-response extraction. End-to-end tests cover application
semantics and persistence. These tests do **not** prove an account can use a
particular model or that a live provider honored a request.

Installed-native localhost fixtures additionally verify request framing and
generation-only behavior without a paid model or real credentials:

```sh
GINKGO_OPENCODE_SMOKE_EXECUTABLE=/absolute/path/to/opencode \
  go test ./internal/ai -run '^TestOpenCodeNativeReal(ImageBytesNoInference|PrivateSystemNoRetention|LocalComplete|Base64RuntimeLimit)$' -count=1 -v
GINKGO_CODEX_NATIVE_SMOKE=1 \
  go test ./internal/ai -run '^TestCodexNativeLoopbackIsolation$' -count=1 -v
GINKGO_CLAUDE_CODE_LOCAL_SMOKE=1 \
  go test ./internal/ai -run '^TestClaudeCodeRealLocalTransportSmoke$' -count=1 -v
```

These tests require the pinned installed binaries and a host that passes their
isolation checks. OpenCode verifies eight byte-identical images without resuming
a runner and system/schema cleanup using an impossible model and a disposable
synthetic saved profile; its read-only database check additionally needs Python 3
with SQLite. Codex verifies an empty tool array, strict schema, and three
images including large local-image inputs. Claude verifies ordinary text and
formatter-only structured output with images and ambient-customization canaries.
They are local transport verification, **not real-account inference**.

OpenCode's full local completion fixture additionally runs the real session
runner against a loopback-only OpenAI-compatible SSE stub using a disposable
profile and fake credentials. It verifies strict note JSON, private system
reload/reset, the `low` variant, an unchanged 3840×2160 PNG just below 4 MiB, and
actual vision completion using a reduced-size copy of a >5 MiB PNG in an ordered
mixed batch. Original store/export hashes remain unchanged. The runtime predicate
test reproduces the upstream Base64 rejection using synthetic buffers in the
installed executable's Bun runtime, without starting OpenCode or accessing auth.

The Codex fixture covers synthetic saved API-key and ChatGPT authentication;
Claude's fixture covers synthetic API-key and OAuth-token environment paths.
Neither proves real subscription entitlement, quota availability, or a live token
refresh exchange. Production login and refresh remain owned by the native CLI.

Opt-in tests against installed executables are separate. A version/isolation
probe is not an authenticated-generation test. A real generation smoke may send
a harmless fixture to a provider and incur a charge; never enable it in the
default test suite or use private notes/screenshots as fixtures.

The shared live test sends only a generated solid-color PNG and a short question
asking for that color. **It uses your native account and may incur a charge.**
Select the executable explicitly; do not enable it in CI by default:

```sh
GINKGO_AI_LIVE_SMOKE=1 \
GINKGO_AI_SMOKE_PROVIDER=opencode-native \
GINKGO_AI_SMOKE_EXECUTABLE=/absolute/path/to/opencode \
GINKGO_AI_SMOKE_MODEL=provider/your-vision-model \
go test ./internal/ai -run '^TestNativeProviderLiveImageSmoke$' -count=1 -v
```

`GINKGO_AI_SMOKE_PROVIDER` also accepts `codex` or `claude-code`.
`GINKGO_AI_SMOKE_VARIANT` is optional. When metadata lacks a vision field, set
`GINKGO_AI_SMOKE_IMAGE_INPUT=supported` only after checking the selected model's
documentation. Codex's dedicated native profile is selected with
`GINKGO_AI_SMOKE_AUTH_HOME`. The same version and isolation gates apply to smoke
tests as production; tests never bypass them to obtain a successful response.

Official contracts used for verification:

- [OpenCode V2 API](https://opencode.ai/v2/docs/api/),
  [OpenAPI](https://opencode.ai/v2/openapi.json),
  [client](https://opencode.ai/v2/docs/build/client), and
  [agents](https://opencode.ai/v2/docs/agents).
- [Codex app-server](https://developers.openai.com/codex/app-server),
  [non-interactive mode](https://developers.openai.com/codex/non-interactive-mode),
  and version-specific schemas generated by the installed executable.
- [Claude Code headless mode](https://code.claude.com/docs/en/headless),
  [CLI reference](https://code.claude.com/docs/en/cli-reference),
  [streaming input](https://code.claude.com/docs/en/agent-sdk/streaming-vs-single-mode),
  and [Agent SDK types](https://code.claude.com/docs/en/agent-sdk/typescript).
