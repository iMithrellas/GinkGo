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
- Interactive list table using Bubble Tea (`ginkgo-cli note list`).

### Sync
- Local outbox queues edits when offline.
- Same permanent storage as offline cache — no special cases.
- Manual one shot or background sync (`ginkgo-cli sync`).
- Bulk note import/export (NDJSON, Markdown directories).

### Notifications (Planned)
- Configurable nudges if no notes are created for N days.
- Delivered via the local daemon; can snooze or mute.

### Storage & Backends
- SQLite (WAL) default for local use.

---

## Install

### Arch Linux (AUR)

```bash
yay -S ginkgo-cli # or AUR helper of your choice
```
- https://aur.archlinux.org/packages/ginkgo-cli

### Docker (sync remote)

Build and run the HTTP replication server using Docker:

```bash
# Clone repo
git clone https://github.com/iMithrellas/GinkGo.git
cd GinkGo

# Build image
docker build -t ginkgo-server .

# Run: map host 8080 and provide an auth token for replication clients
docker run --rm \
  -p 8080:8080/tcp \
  -e GINKGO_AUTH_TOKEN="replace-me" \
  -v ginkgo-data:/data \
  ginkgo-server
```

- Volume `ginkgo-data` persists the server SQLite DB and state under `/data`.

### Manual install

Dependencies:
- Go 1.24+ (per `go.mod`)
- `make`
- `gzip` (for `make install-man`)
- `mandb` (optional, for updating man page cache)
- systemd user service (optional, for `make install-service`)

```bash
git clone https://github.com/iMithrellas/GinkGo.git
cd GinkGo
make build
make install-binary
make install-man      # optional
make install-service  # optional
```

Running without systemd (e.g., Hyprland autostart):

```bash
exec-once = ginkgod
```

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

## Bubble UI

`note list` supports an interactive table powered by Bubble Tea. It is enabled by default, can be overridden with `--output=json/pretty/plain`.

## Roadmap

Active roadmap is tracked in GitHub Issues:

- https://github.com/iMithrellas/GinkGo/issues/1

Upcoming highlights from the roadmap:

- Notification scheduler and delivery.
- Multi-user ACLs for shared namespaces.
- Release packaging (archives, checksums, install script).

## Contributing

Contributions are welcome! Roadmap and open tasks are tracked in GitHub issues. Please run pre-commit locally to keep builds green and diffs clean <3.
