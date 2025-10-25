## Contributing to GinkGo

Thanks for your interest in contributing! This repo aims for small, focused changes with clear intent and good ergonomics for reviewers.

### Quick Start
- Install pre-commit and enable hooks: `pre-commit install`
- Build and test locally: `make build test`
- Keep PRs small and scoped. Include rationale in the description.

### Development
- Prefer simple, composable packages under `internal/`.
- Keep the CLI thin; most logic should live in services and packages.
- Avoid global state; pass dependencies via constructors (see `internal/wire`).
- Write small functions with clear inputs/outputs and error returns.

### Code Style
- Use `go fmt` and `go vet` (see `make fmt vet`).
- Favor early returns and explicit errors.
- Add godoc comments for exported types/functions.

### Commit Hygiene
- Conventional commits style is welcome but not required.
- Reference issues where possible.
- Avoid mixing refactors with functional changes.

### Testing
- Unit tests for non-trivial logic are encouraged.
- CLI behavior should be exercised via package-level functions, not only `cobra.Command`.

### Security & Privacy
- Do not log secrets or tokens. Use redaction helpers where necessary.
- Prefer prepared statements and parameterized queries for DB code.

### Releases
- The `main` branch should remain buildable.
- Tag releases with semantic versions when the project matures.

Thank you for helping improve GinkGo!
