# Provenance POC storage contract

`api.Entry.Provenance` is optional (`*api.Provenance`, JSON `provenance,omitempty`).
It contains ordered `Sources []api.Source` (JSON `sources`). A source contains
`ID string`, `CapturedAt time.Time`, `Kind string` (`voice` or `text`),
`OriginalText string`, and optional `Language`, `STTEngine`, `STTModel` strings.
JSON uses `id`, `captured_at`, `kind`, `original_text`, `language`, `stt_engine`,
and `stt_model`; the last three omit empty values. The application creates IDs,
capture times, and originals, not the model. SQLite stores nullable JSON TEXT.

## Writer policy and limitations

All writers participating in this POC must use the provenance-aware version,
including every synced device. There is no network capability negotiation or
schema-version fence preventing an older binary from opening the database. Do not
roll out to mixed-version writers: an older device can reserialize an entry/event
without sources, irreversibly losing them on devices without a local copy.

As defense in depth, updated SQLite writers treat nil provenance as "preserve",
both for local CAS edits and replication application. Explicit provenance must
retain the exact existing source prefix (capture times compare by instant), with
new sources appended. Dropping, changing, or reordering originals returns an error
wrapping `db.ErrConflict`; it never modifies the entry, FTS, or event snapshot.
This also rejects divergent nonnil source histories during replication rather
than silently losing one side. Strictly older replicated snapshots whose sources
are a prefix of the current history are successful no-ops: current content,
version, and sources remain intact, and other events in the batch still apply.
An absent source history also qualifies as a prefix. An older snapshot with
divergent sources is rejected, not discarded. Equal-version or newer snapshots
must retain the complete current prefix as usual; a higher version with otherwise
identical content still updates the materialized version.
Manual reconciliation is required for divergent histories; this POC does
not merge concurrent source histories. Source removal requires deleting the note.

Sources are not in the body, tags, regex haystack, or FTS. Full entry JSON and
protobuf responses carry them for export/explicit display; ordinary text/Markdown
presenters and AI context builders must continue to use only ordinary note fields.
List results also carry sources (even when bodies are omitted); export callers
must request bodies for a complete entry. Nil provenance preserves old hashes.
Non-nil provenance hashes ordered source fields with length prefixes and UTC
capture times. Nil and empty source lists under a nonnil provenance object hash
equally, and are distinct from absent provenance.
