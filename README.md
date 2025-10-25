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
- Full-text and regex search with filters (`--in body|title|tags`, date ranges).
- Highlighted matches, context lines, count mode.
- Markdown rendering in the terminal (currently minimal; Glamour-based theming planned).
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
- Postgres backend (optional) for remote sync/multi-device deployments.

---

## Why the Name GinkGo

The **Ginkgo tree** is a symbol of memory, resilience, and longevity — a natural metaphor for journaling. The capitalized “Go” highlights the language of implementation.

To avoid confusion with the Go testing framework of the same name, the binary/package is called **`ginkgo-cli`**, while the project identity remains **GinkGo**.

---

## Shell Completions

```bash
ginkgo-cli completion generate bash
ginkgo-cli completion generate zsh fish
```

## Bubble UI

`note list` supports an interactive table powered by Bubble Tea. It is enabled by default (no build tags required):

```bash
make build
ginkgo-cli note list --bubble
```

- The first build will fetch Charmbracelet deps automatically via Go modules.
- Use `--bubble` for the interactive UI, or omit it for plain tab-separated output.

## Roadmap

Active roadmap is tracked in GitHub Issues:

- https://github.com/iMithrellas/GinkGo/issues/1

Upcoming highlights from the roadmap:

- Glamour-based Markdown rendering for note bodies (themeable).
- `note list --json` machine-readable output.
- Time range filters for list/search (absolute timestamps and relative expressions like `-1d`, `-1d to -3d`).
- Background sync service and export commands.
- `note delete` and additional quality-of-life improvements.

## Contributing

Contributions are welcome! Roadmap and open tasks are tracked in GitHub issues. Please run pre-commit locally to keep builds green and diffs clean <3.
