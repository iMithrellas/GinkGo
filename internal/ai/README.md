# OpenCode provider compatibility

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
