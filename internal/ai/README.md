# AI note terminology correction

AI note prompts ask for contextual correction of jargon, technical names and
acronyms in generated titles, new body material and proposed tags. A bounded
`input_kind` (`text` or `voice`) tells the model when the input is an STT transcript
so it can consider phonetic substitutions, word boundaries and spoken acronyms.
Capture metadata, model paths and earlier originals are not added to the prompt.

The model uses the input and existing bounded note/tag context, preferring relevant
local spellings. Familiar canonical spellings are allowed when the intended term
is clear; ambiguous or unfamiliar names should be retained rather than guessed.
Literal code, identifiers, commands, paths, URLs, versions and numbers are to be
preserved, along with language, meaning, negation and uncertainty. These are model
instructions, not deterministic spell-check rules or measured accuracy guarantees.

Corrections affect AI-generated note content only. Exact input remains in
provenance; raw mode/fallback remains verbatim, explicit title/tag overrides keep
their existing behavior, and expansion still appends without rewriting old body
text. No extra retrieval, model call or configuration is needed.

Each expansion is stored under an application-generated `## YYYY-MM-DD HH:MM UTC`
subsection. The minute-resolution timestamp uses the daemon's UTC clock;
consecutive same-minute expansions share the final timestamp subsection. The model
returns only the addition, not a date/time heading, so user input cannot choose it.

# AI note topic-tag policy

Successful AI creation or expansion requires a **nonempty final normalized tag
set**, not a newly generated tag. Creation merges global defaults, explicit tags,
and model tags; expansion merges current target tags, explicit tags, and model
tags (global defaults are not added to expansions). Normalization trims,
lowercases, removes blanks, and deduplicates in merge order. An empty final set
invalidates the draft and saves the exact incoming input as a new raw note, with
no filler tag and no target mutation. Existing tagged notes need no new tag.
Malformed drafts still fail even when user/default/current tags are sufficient.
Raw fallback retains ordinary default/explicit behavior and may be untagged.

Models should prefer relevant existing topic names, but may introduce meaningful
new tags supported by the input. Unrelated suggestions, filler, and unsupported
sensitive inferences are discouraged. Model tags remain limited to 64 entries
and 128 bytes per name; normal application tags/defaults are not restricted or
rewritten by this AI-only policy.

## Scoped vocabulary and budgets

SQLite implements the optional `db.TagVocabulary` capability without changing
`EntryRepo`. It reads only tag names, note IDs and modification dates from the
exact selected namespace (even an empty namespace does not mean all namespaces).
It never retrieves extra bodies, descriptions or provenance. Forced creation
gets vocabulary hints too, without broadening candidate-note context. Repositories
without this capability, and failed lookups, simply omit vocabulary hints.
Request cancellation still forbids saving; lookup failure cannot bypass tag
validation.

Names are normalized and blanks/names over 128 bytes are omitted. Rank order:

1. Relevant names first: at least one whole letter/digit token of the tag occurs
   among input tokens, case-insensitively; punctuation separates tokens.
2. Descending frecency: `log2(1 + frequency) + 1/(1 + age_days/30)`, where
   frequency is the number of distinct notes carrying the normalized tag and
   age is time since the most recent carrying note's modification. Future dates
   clamp to age zero. A 30-day age halves the recency bonus.
3. Normalized tag name ascending breaks ties deterministically.

Return at most 40 names, ranked across the full namespace before truncation.
Only names enter the prompt. Known, explicit and applicable default hints share
a separate **4 KiB JSON-encoded budget** (including escaping and field overhead),
in addition to the existing bounded candidate-note and input budgets. Explicit
hints precede defaults, then ranked vocabulary; each name is at most 128 bytes,
with at most 64 explicit/default hints each. Truncation affects hints only, never
the final merge or validation. Validation precedes writes; CAS conflicts produce
the ordinary exact-raw fallback without retrying or overwriting the target.

# OpenCode provider compatibility

Two provider types are available: `opencode` (the existing stateless adapter) and
`opencode-session` (the supported session generation-only API). Both run a private
server with fixed argv and isolated configuration; neither attaches to the shared
OpenCode service or invokes a normal coding-agent prompt.

`NewOpenCode` runs a private server with isolated configuration, fixed argv,
tool-free stateless generation, bounded input/output, and process-group cleanup.
It never connects to the shared daemon or loads project/user plugin configuration.
The caller supplies an executable path and, preferably, an explicit model.

| Mode | Version | Authentication |
| --- | --- | --- |
| Default saved-auth | >=2.0.25; smoke-tested on 2.0.30 | Existing ordinary provider logins; Console accounts rejected before plugin activation |
| `EphemeralAuth: true` | Smoke-tested on 2.0.19 | Fresh disposable OpenCode data/state; non-Console provider API-key environment variables, e.g. `OPENAI_API_KEY` |

For the installed 2.0.19 executable:

```go
provider, err := ai.NewOpenCode(ai.OpenCodeOptions{
    Executable: "/home/mithrel/.opencode/bin/opencode",
    Model: "openai/your-selected-model",
    EphemeralAuth: true,
})
```

The caller/environment must supply a valid provider key in ephemeral mode.
No keys should be put into prompts or argv. After upgrading to a compatible
version, omit `EphemeralAuth` to reuse ordinary saved provider logins. The adapter
does not upgrade OpenCode or alter user configuration.

## Why saved auth is refused on 2.0.19

Verified against the installed authenticated private OpenAPI and the `v2.0.19`
release source:

- There is no global `GET /api/credential` or auth export command.
- `auth list` uses model readiness and `/api/integration`; these initialize
  plugins before returning account metadata. They cannot safely preflight Console.
- `opencode.provider.opencode` is non-removable, resolves its active credential
  during initialization, and may apply organization policies/MCP configuration.
- Remote well-known configuration can supply plugin modules. Version 2.0.19
  imports modules before processing later removals; `plugin:*` policy does not
  guard imports until 2.0.25. Merely querying accounts and rejecting afterwards
  would be too late.

No supported pre-initialization interface can selectively export ordinary
credentials in 2.0.19. The adapter does not read/copy the credential database or
weaken isolation. The global credential API appeared in 2.0.20, but saved-auth
mode also requires 2.0.25's pre-import policy enforcement.

## Explicit no-inference smoke tests

```sh
GINKGO_OPENCODE_SMOKE_EXECUTABLE=/absolute/path/opencode \
go test ./internal/ai -run '^TestOpenCodeRealProtocol$' -v -count=1

# Requires >=2.0.25; seeds synthetic credentials through the supported API.
GINKGO_OPENCODE_SMOKE_EXECUTABLE=/absolute/path/opencode \
go test ./internal/ai -run '^TestOpenCodeRealSavedAuthProtocol$' -v -count=1
```

Both use disposable HOME/XDG/database paths and an intentionally nonexistent
provider/model, which must return explicit unknown-model rejection. The second
also verifies that a synthetic saved ordinary provider account is accepted and
a synthetic Console account is rejected. Neither test verifies real-login
validity, contacts a model for inference, or reads actual user accounts/notes.

## Session generation adapter

Configure a named provider with `type = "opencode-session"` and the same
`executable`, `model`, and optional `ephemeral_auth` settings as the stateless
adapter. `ai.timeout` bounds each request. The Go constructor
`NewOpenCodeSession(OpenCodeSessionOptions)` also accepts the shared `Options`
byte/time limits and a `TempDir` for private work files.

This adapter uses supported session create/generate/delete HTTP routes, **not**
`opencode run` or session prompt. It creates one random `ses_ginkgo_...` session
in a temporary working directory, invokes generation without adding conversation
messages, then deletes only that owned session. Saved-auth mode uses the ordinary
profile's database: creating/deleting this temporary session is a write to that
database, even though existing sessions are not reused or modified. With
`ephemeral_auth = true`, both auth and sessions use a disposable database instead.

Tool definitions are filtered before constructing the model request through
deny-all agent/session permissions; generation never enters the tool-execution
loop. Private configuration and project-discovery controls additionally suppress
discovered instructions, skills, MCP and custom plugins. Only the required built-in
model/provider and agent-config plugins are selected. Console accounts are rejected
before integration initialization, as in the stateless adapter. Installed
`/etc/opencode` configuration is refused because it could override isolation.

The minimum supported stable version is **2.0.25**, including ephemeral-auth
session mode. Isolation paths were source-checked at v2.0.25 (`b44eea9`) and
v2.0.31 (`22728d6`). A real **v2.0.31** no-inference protocol smoke passed with a
disposable profile, synthetic saved credential, and an intentionally impossible
model. This does **not** establish real saved-login validity or preferred model
compatibility; provider generation-only restrictions can still reject requests.

Session deletion has an independent two-second cleanup deadline, followed by
private process-group cleanup. Cleanup uses the locally generated session ID,
including when the creation response is malformed or lost. A process crash or
failed deletion may leave a session titled `GinkGo ephemeral generation` in the auth
database. Cleanup failures never turn an unsuccessful request into success. The
adapter does not sweep other sessions or copy/export the credential database.

```sh
GINKGO_OPENCODE_SESSION_SMOKE_EXECUTABLE=/absolute/path/opencode \
go test ./internal/ai -run '^TestOpenCodeRealSessionNoInference$' -v -count=1
```

Routine fake-executable tests cover session ownership, output and process-log
bounds, malformed output, provider errors, Console rejection, cancellation, and
bounded deletion. Hardware/model calls remain outside routine tests.
