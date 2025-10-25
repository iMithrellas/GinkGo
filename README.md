# GinkGo

A resilient, local-first journaling tool with a client–server architecture. Designed for quick one-liner notes or full Markdown entries in your editor of choice, with tags, namespaces, offline sync, and regex search. Built around an always-on daemon that unifies local and remote workflows, GinkGo ensures your notes are safe, consistent, and always accessible.

---

## Core Purpose

- **Primary**: Provide a lightweight journaling system that works seamlessly both offline and online, with consistent behavior whether running purely local or syncing to remote servers.
- **Secondary**: Support multiple users and namespaces on one server, enabling separation of contexts (e.g., `work`, `personal`, `ideas`) and collaboration while preserving strong consistency guarantees.

Entries can be created as quick one-liners or rich Markdown notes via `$EDITOR`. All entries support tags, can be searched with regex, and are rendered prettily in the terminal.

---

## Core Architecture

- **Local Daemon**: A background process (`ginkgod`) always runs locally, handling storage, search, and notifications. CLI commands (`ginkgo-cli`) talk to it via IPC.
- **Event Log Storage**: Entries are stored as immutable events with versions, enabling safe replication and offline buffering.
- **Replication**: The local daemon can sync events to one or more remote servers. Offline and online use the same code paths: if peers aren’t reachable, events stay in the local log until connectivity returns.
- **Consistency**: Updates use CAS (compare-and-swap). Conflicts are rare; mismatches are refused rather than silently overwritten.

---

## Features

### Journaling
- Quick one-liner notes from the CLI.
- Full Markdown entries opened in `$EDITOR` (sudoedit-style flow).
- Multi-line stdin input for piping notes or imports.
- Tags (`#work`, `#personal`) with tag cloud and filtering.
- Namespaces per user (separate journals under one account).

### Search & Rendering
- Full-text regex search with filters (`--in body|title|tags`, date ranges).
- Highlighted matches, context lines, count mode.
- Pretty Markdown rendering directly in the terminal.
- Export entries to Markdown, JSON, or NDJSON.

### Multi-User & Namespaces
- Multiple accounts per server.
- Namespaces per user (e.g., `work`, `personal`, `journal`).
- Per-namespace ACLs for shared environments.

### Offline & Sync
- Local outbox queues edits when offline.
- Same permanent storage as offline cache — no special cases.
- Manual or background sync (`ginkgo-cli sync --daemon`).
- Bulk note import/export (NDJSON, Markdown directories).

### Notifications
- Configurable nudges if no notes are created for N days.
- Delivered via the local daemon; can snooze or mute.
- Server-side scheduling ensures consistency across devices.

### Storage & Backends
- SQLite (WAL) default for local use.
- Postgres for multi-user or remote servers.
- Pluggable backends for event log and search index.

---

## Why the Name GinkGo

The **Ginkgo tree** is a symbol of memory, resilience, and longevity — a natural metaphor for journaling. The capitalized “Go” highlights the language of implementation.
To avoid confusion with the Go testing framework of the same name, the binary/package is called **`ginkgo-cli`**, while the project identity remains **GinkGo**.

---

## Shell Completions

Completions can be generated to `$XDG_DATA_HOME/ginkgo/completions` (fallback: `~/.local/share/ginkgo/completions`).

```bash
ginkgo-cli completion generate bash
ginkgo-cli completion generate zsh fish
```

## Contributing

Contributions are welcome! Roadmap and open tasks are tracked in GitHub issues. Please run pre-commit locally to keep builds green and diffs clean <3.
