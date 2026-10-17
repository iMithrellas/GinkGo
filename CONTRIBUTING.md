# Contributing

## Build and test

Use the Go version in [go.mod](go.mod). From the repository root:

```sh
go fmt ./...
go test ./...
go vet ./...
make build
```

Run a package test for focused work, for example `go test ./internal/db`.
Integration tests live in `tests/`. `make run` and `make dev` install binaries
and restart the user service; `make build` only builds.

These smoke scripts use disposable state and fake providers/audio:

```sh
python3 scripts/smoke-ai-poc.py
python3 scripts/smoke-voice-poc.py
python3 scripts/smoke-voice-listening.py
make test-voice-bundle
```

Optional tests require explicitly provisioned assets:

| Test | Inputs |
| --- | --- |
| OpenCode protocol tests in `internal/ai` | `GINKGO_OPENCODE_SMOKE_EXECUTABLE`; session tests use `GINKGO_OPENCODE_SESSION_SMOKE_EXECUTABLE`. These exercise a real binary without model inference. |
| Native fixtures in `internal/sherpavoice` | `sherpa_onnx` build tag, native libraries, and `GINKGO_NATIVE_FIXTURE_CONFIG`. |
| `TestSherpaNativeCLIWorkflow` in `tests/` | `GINKGO_VOICE_E2E_CONFIG` and `GINKGO_VOICE_E2E_CASES`; optional `GINKGO_VOICE_E2E_ARTIFACTS` retains private evidence. |

See test definitions for fixture formats and [native setup](docs/voice.md#native-setup)
for build requirements. Keep recorded audio and retained evidence outside the repo.

## Change guidelines

Keep CLI handlers focused on parsing and I/O. Put reusable logic in `internal/`,
pass dependencies through constructors, and return contextual errors. Document
exported APIs with GoDoc and add deterministic tests for behavior changes.
Use parameterized SQL and keep secrets out of logs.

Check [data compatibility](docs/internals.md#note-provenance) and
[voice protocols](docs/voice-protocol.md) when changing storage or transports.
`make proto` regenerates IPC code and requires `protoc` and `protoc-gen-go`.

Edit authored guides in `docs/`. Generate CLI Markdown and man pages with
`make docs`; avoid unrelated date-only changes. When moving a guide, update
incoming links, the [index](docs/README.md), and packaging references.
