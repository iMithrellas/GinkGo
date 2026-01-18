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
- **Replication (Optional)**: The local daemon can sync events to one or more remote servers. Events carry namespace IDs and payloads; servers treat payloads as opaque.
- **Security**: Namespace payloads can be end-to-end encrypted (E2EE) and replication events can be signed for trusted signers.
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
- Optional E2EE for new namespaces with keyring support.

### Bubble UI
- `note list` supports an interactive table powered by Bubble Tea. It is enabled by default, can be overridden with `--output=json/pretty/plain`.

#### How to Sync Between Two Clients
1. Run the sync server (see Docker section) and pick a shared auth token.
   - This is a shared secret that all clients must know.
   - Set it on the server as `GINKGO_AUTH_TOKEN`.
   - Use the exact same value in each client config under `remotes.origin.token`.
   - Optional: configure `namespaces.<name>.trusted_signers` on the server to require signed replication events; only listed signer public keys are accepted.
2. On both clients, configure the same remote URL + token.
3. Keep the daemon running; it syncs in the background after local changes.
4. Optional: use `ginkgo-cli sync` to trigger an immediate foreground sync.

Example config:
```
[remotes.origin]
enabled = true
url = "https://sync.example.com"
token = "replace-me"
```

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
- The server stores opaque payloads; enable E2EE in client namespaces to keep payload content encrypted end-to-end (use TLS if you want transport encryption too).
- If you configure `trusted_signers` on the server, replication events must be signed by an approved key.

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

## Neovim LSP

You can use the `ginkgo-lsp` binary for tag completion in Neovim. The LSP expects `*.ginkgo.md` temp files and uses the namespace encoded in the filename.

```lua
vim.api.nvim_create_autocmd({ 'BufRead', 'BufNewFile' }, {
  pattern = '*.ginkgo.md',
  callback = function()
    vim.bo.filetype = 'markdown'
    vim.lsp.start {
      name = 'ginkgo-lsp',
      cmd = { '/usr/bin/ginkgo-lsp' },
      root_dir = vim.fn.getcwd(),
    }
  end,
})
```

## Other Editors

For VSCode or other editors, use a generic LSP client extension/plugin and point it at `/usr/bin/ginkgo-lsp` for the `*.ginkgo.md` file extension. If your editor lacks a generic LSP client, you can write a minimal client wrapper to launch the binary over stdio.

## Make Targets

- `make build`: build `ginkgo-cli` into `./build/`
- `make install`: build + install binary + install man pages
- `make install-binary`: build output symlinked to `~/.local/bin/ginkgo-cli`, `~/.local/bin/ginkgod`, and `~/.local/bin/ginkgo-lsp`
- `make install-service`: install the systemd user service file
- `make reload-service`: reload systemd user daemon and restart ginkgo service
- `make run`: build, install, and restart the service (one-shot)
- `make dev`: alias for `make run`
- `make docs`: generate Markdown + man pages
- `make install-man`: install man pages
- `make uninstall-man`: remove man pages

## Roadmap

Active roadmap is tracked in GitHub Issues:

- https://github.com/iMithrellas/GinkGo/issues/1

## Contributing

Contributions are welcome! Roadmap and open tasks are tracked in GitHub issues. Please run pre-commit locally to keep builds green and diffs clean <3.
