# Write, find, and move notes

## Write and revise

```sh
ginkgo-cli note add "Read about SQLite WAL" --tags reading,dev
EDITOR=nvim ginkgo-cli note add
ginkgo-cli note list --output plain
ginkgo-cli note show NOTE_ID
ginkgo-cli note edit NOTE_ID
```

Without a title, `note add` opens a temporary `*.ginkgo.md` file. Set `Title:` and
comma-separated `Tags:` fields, then write Markdown below `---`. `$VISUAL` takes
precedence over `$EDITOR`; otherwise GinkGo looks for nvim, vim, or vi. Editor saves
use the version originally opened. If another writer changed the note, inspect the
latest version and retry; `note edit --force` reopens it.

Use `-n` / `--namespace` to select a configured namespace. For example:

```sh
ginkgo-cli config namespace init -n work
ginkgo-cli note add "Review the design" -n work --tags planning
```

Restart the daemon after changing its namespace configuration. See
[configuration](config.md#namespace-and-general-settings) for setup details.

## List and search

```sh
ginkgo-cli note list --since 7d --tags-any reading,dev --output plain
ginkgo-cli note search fts '"morning walk"' --tags-all health
ginkgo-cli note search regex '(?i)sqlite|journal' --output json
```

FTS queries use SQLite FTS5 syntax over title, body, and tags; quote the whole query
for the shell. Results sort newest first. Regex uses Go syntax
and is case-sensitive unless `(?i)` is used. Regex search prefilters candidates
with an FTS token and limits candidates per page, so it can omit matches. Use
simple token-oriented patterns and filters; it cannot prove sensitive text is
absent. Neither search mode indexes captured original sources.

Both list and search accept `--namespace`, `--tags-any`, `--tags-all`, `--since`,
and `--until`. Time filters accept relative values such as `2h` or `3d`, or absolute
timestamps such as `2026-10-17T14:30`. Output modes are `plain`, `pretty`, `json`,
and `ndjson`; list also supports its default interactive `tui`. `--noheaders` hides
plain output headers. `--page-size` controls paging and defaults to
`export.page_size`. Commands may return multiple pages.

## Export, import, and originals

```sh
ginkgo-cli note list --export --output ndjson > journal.ndjson
ginkgo-cli import journal.ndjson
ginkgo-cli note show NOTE_ID --original
```

`note list --export` includes bodies; use the selected namespace and filters, and
repeat for other namespaces. List results may include captured sources even when
bodies are omitted, so request `--export` for complete entries. JSON and NDJSON
can contain original text that is no longer visible in the edited note. Ordinary
text/Markdown display and search omit originals. `note show --original`
prints captured text; with JSON or NDJSON output it returns the provenance object.

Import accepts a JSON array or NDJSON. It creates new notes through the daemon,
assigning new IDs and using the selected/default namespace when one is absent.
Conflicts/errors are counted as skipped. Use an SQLite-aware backup procedure to
preserve IDs, versions, and the full database for recovery.

Notes, indexes, event snapshots, captured originals, editor temp files, exports,
and backups are plaintext on local storage. Replication E2EE does not encrypt the
local SQLite database. Deleting a note removes its current source history; copies
can remain in prior events, remote logs, exports, or backups. Protect the device and
any files containing journal data.

## Shell and editor completion

Generate a completion script, then load it through your shell's completion setup:

```sh
ginkgo-cli completion generate bash > ginkgo-completion.bash
```

Zsh and Fish are also supported. Tag and namespace suggestions query the daemon;
tag completion accepts comma-separated lists and fuzzy matches.

<details>
<summary>Neovim tag completion</summary>

Build `ginkgo-lsp` with `make lsp` and put it on `PATH`. The editor helper uses
stdio and reads the namespace from the `*.ginkgo.md` filename:

```lua
vim.api.nvim_create_autocmd({ 'BufRead', 'BufNewFile' }, {
  pattern = '*.ginkgo.md',
  callback = function()
    vim.bo.filetype = 'markdown'
    vim.lsp.start {
      name = 'ginkgo-lsp',
      cmd = { vim.fn.exepath('ginkgo-lsp') },
      root_dir = vim.fn.getcwd(),
    }
  end,
})
```

Other editors can attach a generic LSP client to the same file pattern.

</details>
