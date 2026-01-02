# GinkGo

A resilient, local-first journaling tool with a simple CLI and optional client–server sync. Inspired by the excellent [jrnl](https://github.com/jrnl-org/jrnl), but written in Go for speed, portability, and extensibility.

Designed for quick one-liner notes or full Markdown entries in your editor of choice, with tags, namespaces, offline sync, and powerful search (full-text and regex). GinkGo runs around an always-on daemon that unifies local and remote workflows, ensuring your notes are safe, consistent, and always accessible.

---

## Core Purpose

- **Primary**: Provide a lightweight, CLI-driven journaling system that works seamlessly offline and can sync to a remote server when desired.
- **Optional**: Support syncing across devices via a server backend. Multi-user support and namespaces exist, but the main focus is a personal journaling workflow that just works.

Entries can be created as quick one-liners or rich Markdown notes via `$EDITOR`. All entries support tags, can be searched with regex, and are rendered cleanly in the terminal.

---

## Core Architecture

- **Local Daemon**: A background process (`ginkgod`) always runs locally, handling storage, search, and notifications. CLI commands (`ginkgo-cli`) talk to it via IPC.
- **Event Log Storage**: Entries are stored as immutable events with versions, enabling safe replication and offline buffering.
- **Replication (Optional)**: The local daemon can sync events to one or more remote servers. If peers aren’t reachable, events stay in the local log until connectivity returns.
- **Consistency**: Updates use CAS (compare-and-swap). Conflicts are rare; mismatches are refused rather than silently overwritten.

---

## Features

### Journaling
- Quick one-liner notes from the CLI.
- Full Markdown entries opened in `$EDITOR` (sudoedit-style flow).
- Multi-line stdin input for piping notes or imports.
- Tags (`#work`, `#personal`) with tag cloud and filtering.
- Optional namespaces (e.g., `work`, `personal`, `ideas`).

### Search & Rendering
- Full-text and regex search with date range and tag filters.
- Markdown rendering in the terminal for single-entry “pretty” output (Glamour-based).
- Export entries to Markdown, JSON, or NDJSON.
- Optional interactive list table using Bubble Tea (`ginkgo-cli note list --bubble`).

### Sync
- Local outbox queues edits when offline.
- Same permanent storage as offline cache — no special cases.
- Manual or background sync (`ginkgo-cli sync --daemon`).
- Bulk note import/export (NDJSON, Markdown directories).

### Notifications
- Configurable nudges if no notes are created for N days.
- Delivered via the local daemon; can snooze or mute.

### Storage & Backends
- SQLite (WAL) default for local use.

---

## Why the Name GinkGo

The **Ginkgo tree** is a symbol of memory, resilience, and longevity — a natural metaphor for journaling. The capitalized “Go” highlights the language of implementation.

To avoid confusion with the Go testing framework of the same name, the binary/package is called **`ginkgo-cli`**, while the project identity remains **GinkGo**.

---

## Shell Completions

GinkGo supports dynamic shell completions for namespaces and tags, including fuzzy matching for tag lists (e.g., typing `proj` suggests `project`). It works for comma-separated lists too (e.g. `work,proj` -> `work,project`).

```bash
ginkgo-cli completion generate bash
ginkgo-cli completion generate zsh fish
```

## Make Targets

- `make build`: build `ginkgo-cli` into `./build/`
- `make install`: build + install binary + install man pages
- `make install-binary`: build output symlinked to `~/.local/bin/ginkgo-cli` and `~/.local/bin/ginkgod`
- `make install-service`: install the systemd user service file
- `make reload-service`: reload systemd user daemon and restart ginkgo service
- `make run`: build, install, and restart the service (one-shot)
- `make dev`: alias for `make run`
- `make docs`: generate Markdown + man pages
- `make install-man`: install man pages
- `make uninstall-man`: remove man pages

## QUIC (Experimental)

An experimental QUIC endpoint and ping utility are available to explore remote connectivity and future sync transport.

Start a QUIC server (TLS required; choose one method):

```bash
# 1) ACME via CertMagic (Let's Encrypt by default)
ginkgo-cli quic serve \
  --domain sync.example.com \
  --email you@example.com \
  --http-addr :80                   # serve HTTP-01 challenge

# Options:
#   --acme-ca <dirURL>              # custom ACME directory URL
#   --storage-dir <path>            # cert storage (default XDG cache)
#   --enable-tls-alpn --alpn-port 443  # enable TLS-ALPN-01 challenge

# 2) ZeroSSL API (no ACME challenge)
ginkgo-cli quic serve \
  --domain sync.example.com \
  --zerossl-api-key <API_KEY>

# 3) Bring-your-own certificate
ginkgo-cli quic serve \
  --cert /etc/ginkgo/tls/fullchain.pem \
  --key  /etc/ginkgo/tls/privkey.pem

# 4) Self-signed (testing only; not recommended)
ginkgo-cli quic serve --insecure-self-signed
```

Ping a server and check RTT:

```bash
ginkgo-cli quic ping 127.0.0.1:7845                 # skip verify (no SNI)
ginkgo-cli quic ping sync.example.com:7845 \
  --server-name sync.example.com                    # verify cert
```

Notes:

- QUIC server listens on `--addr` (default `:7845`).
- ACME HTTP-01 requires `:80` reachable for the configured domain (or a reverse proxy).
- Non–Let’s Encrypt ACME providers and the ZeroSSL API path are currently untested.

### Docker

Build and run the QUIC server with ACME (Let’s Encrypt) using Docker:

```bash
# Build image
docker build -t ginkgo-quic .

# Run: map host 80 -> container 8080 for HTTP-01, and expose QUIC UDP 7845
docker run --rm \
  -p 80:8080/tcp \
  -p 7845:7845/udp \
  -v ginkgo-data:/data \
  ginkgo-quic \
  --domain sync.example.com \
  --email you@example.com \
  --addr :7845 \
  --http-addr :8080
```

- Volume `ginkgo-data` persists CertMagic certificate storage (`$XDG_CACHE_HOME`, default `/data/cache`).
- Adjust `--acme-ca`, `--storage-dir`, or use `--zerossl-api-key` as needed.

## Bubble UI

`note list` supports an interactive table powered by Bubble Tea. It is enabled via `--output=tui` (no build tags required):

```bash
make build
ginkgo-cli note list --output=tui
```

- The first build will fetch Charmbracelet deps automatically via Go modules.
- Use `--output=tui` for the interactive UI, or omit it for plain tab-separated output.

## Roadmap

Active roadmap is tracked in GitHub Issues:

- https://github.com/iMithrellas/GinkGo/issues/1

Upcoming highlights from the roadmap:

- Notification scheduler and delivery.
- Multi-user ACLs for shared namespaces.
- Release packaging (archives, checksums, install script).

## Contributing

Contributions are welcome! Roadmap and open tasks are tracked in GitHub issues. Please run pre-commit locally to keep builds green and diffs clean <3.
