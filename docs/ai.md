# AI notes

`ginkgo-cli ai note` can create a note or append to one. AI is disabled by default.

## Configure a provider

Merge the settings below into your [configuration](config.md), then restart the
daemon. Provider names are arbitrary; select one with `ai.provider` or `--provider`.

```toml
[ai]
enabled = true
provider = "opencode"
timeout = "120s"

[ai.notes]
context_limit = 8

[ai.providers.opencode]
type = "opencode"
executable = "/absolute/path/to/opencode"
model = "provider/your-model"
```

OpenCode saved authentication requires a stable 2.x release >=2.0.25. The daemon
needs access to that profile's auth store. Saved Console credentials are rejected
because their remote configuration can weaken local isolation. Use a separate
profile without Console or another provider.

For API-key authentication, set `ephemeral_auth = true` in the provider table and
supply the provider's API-key variable to the daemon environment. Stateless mode
also supports OpenCode 2.0.19 this way. GinkGo starts OpenCode with tools, plugins,
hooks, MCP, and discovered instructions disabled.

<details>
<summary>Session generation, OpenAI-compatible endpoints, and wrappers</summary>

Set `type = "opencode-session"` to use OpenCode's session-generation endpoint.
It requires stable 2.x >=2.0.25, including with ephemeral authentication. A request
creates a session named `GinkGo ephemeral generation` and deletes it afterward.
In saved-auth mode, a crash or cleanup failure can leave that session and its
input in the ordinary OpenCode database.

For a local or remote OpenAI-compatible endpoint:

```toml
[ai.providers.local]
type = "openai-compatible"
base_url = "http://127.0.0.1:11434/v1"
model = "your-installed-model"
api_key_env = "GINKGO_MODEL_API_KEY"
```

`base_url` includes the API prefix; GinkGo appends `/chat/completions`. The API key
is optional when the endpoint permits anonymous access. For remote services,
use HTTPS. Responses containing tool calls or truncated completions are rejected.

An explicitly trusted generation wrapper can use `type = "cli"`, `executable`,
and a fixed `args` array. It reads one `{"system":"...","prompt":"..."}` object
from stdin and writes one `{"text":"..."}` response to stdout. Keep diagnostics
on stderr. The wrapper must disable tools/integrations, start fresh tasks, and
keep work in its foreground process group so cancellation can terminate it.

</details>

Credentials come from the daemon's environment or OpenCode auth store. Avoid
putting them in TOML, command arguments, or logs. A service can have a different
`HOME`, `PATH`, and environment from your shell; verify those settings without
printing secrets.

## Create and expand

```sh
printf '%s\n' 'A short thought to organize.' \
  | ginkgo-cli ai note --stdin --new

printf '%s\n' 'A follow-up detail.' \
  | ginkgo-cli ai note --stdin --note NOTE_ID --namespace work
```

`--new` and `--note` are mutually exclusive. Expansion preserves the existing
body and appends generated material under dated sections. Without either flag,
the provider may select a target from up to `ai.notes.context_limit` recent notes
in the current namespace. Only those notes are eligible.

Original input is retained separately from the generated body. See
[originals and exports](usage.md#export-import-and-originals) to inspect it.
[Voice commands](voice.md) can use the same providers when saving in AI mode.

Use `--image FILE` for explicit image context, or stage originals with
`context attach` / `context capture`. [Image context](image-context.md) describes
model capability checks, prompt-controlled use, original storage, and sync.

To customize cleanup and terminology, edit `ai.notes.system_prompt`. Start from
the generated default and preserve its JSON response contract: `action`, `title`,
`body`, `tags`, and `target_id` for expansion. The final merged tag set must be
nonempty. See [draft validation](internals.md#ai-write-boundary) for the rules.

## Fallbacks and errors

Disabled AI, provider failures, timeouts, invalid drafts, and expansion conflicts
save the exact input as a new raw note. Inspect `action`, `entry.id`, and
`fallback_reason` in the result. Cancellation ends the request without a fallback.

| Symptom | Check |
| --- | --- |
| Raw note after changing settings | Confirm `ai.enabled`, provider name, and the daemon's config; restart after edits. |
| Authentication or model error | Check daemon profile/environment, OpenCode version, and model access. |
| Free-tier generation restriction | Choose a model/provider that permits the selected generation endpoint. |
| Expansion produced a new note | Inspect the fallback reason; a concurrent edit or invalid draft can cause this. |
| Timeout | Check provider connectivity and `ai.timeout`. |

Provider requests can expose input and candidate note bodies independently of
sync encryption. Local copies follow the [storage rules](usage.md#export-import-and-originals).
