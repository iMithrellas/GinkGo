# Internal contracts

## Package ownership

- `internal/cli` parses commands and presents results. `internal/daemon` owns
  long-running workflows; `internal/wire` assembles dependencies.
- `internal/notes` owns note operations, `internal/db` persistence, and
  `internal/sync` replication. Keep sync payloads opaque and preserve the
  security boundaries in [sync](sync.md).
- `internal/ai` produces and validates drafts through the notes service.
- `internal/voice` owns on-demand/resident transcription, capture, and generic
  note-gating primitives. `internal/sherpavoice` owns the optional native helper
  adapter. `internal/voiceworker` supervises its private process protocol;
  `internal/voiceevents` is a distinct, public read-only observation socket.

Constructors should remain lazy: disabled features must not probe files, devices,
providers, or start processes.

## Note provenance

Provenance stores ordered captured originals beside edited note content. Its API
shape is `api.Entry.Provenance *api.Provenance`, with ordered `Sources`; each
source records `ID`, `CapturedAt`, `Kind` (`voice` or `text`), `OriginalText`,
and optional `Language`, `STTEngine`, `STTModel`. SQLite stores nullable JSON
TEXT. The application assigns source identity, capture time, and original text
before passing input to a model.

Sources are append-only. Editors and replication writers must use compatible
versions. There is no capability negotiation: an older writer can erase source
history by reserializing an entry. Updated writers interpret nil provenance as
preserve. Explicit history must retain the exact current source prefix, including
order and all values; capture times compare by instant. Changes that drop,
rewrite, or reorder sources fail with `db.ErrConflict` without changing entry,
FTS, or event state. Strictly older snapshots whose source history is a prefix
are successful no-ops; divergent histories conflict. Concurrent histories need
manual reconciliation; replication does not merge them.

Compatibility is also encoded in hashes. Nil provenance preserves the legacy
hash exactly. Non-nil provenance hashes ordered source fields with length
prefixes and UTC capture times. Nil and empty `Sources` under a non-nil object
hash alike, but differ from nil provenance. Do not alter these rules without a
versioned migration plan. Full JSON/protobuf and replication carry source history;
ordinary note display, search, and AI context omit it. See
[storage and export handling](usage.md#export-import-and-originals).

## AI provider boundary

All backends implement `ai.Provider.Complete(context.Context, Request)`. The
request keeps application instructions separate from user/context text and
carries ordered, validated image inputs. Optional model/variant selection and a
generic JSON schema belong to the provider contract, not to the voice or CLI
protocol. Provider adapters own wire encoding, authentication, model resolution,
execution controls, and extraction of the completed answer. Reasoning, tool
events, progress, and partial responses are not note drafts.

`CapabilityProvider` discovers metadata without submitting note content or
performing inference. Effective image support is the intersection of model,
transport, and configuration: `supported`, `unsupported`, or `unknown`. An
explicit declaration may fill missing model metadata, but cannot make a
text-only transport accept images. Unknown support skips visual submission,
not text processing or original retention. See [image context](image-context.md).

Providers are siblings: none invokes another coding agent. Runtime ownership is
explicit and optional; an ordinary HTTP adapter does not need a session API.
Persistent runtimes are daemon-owned, but each note gets a fresh generation
context. Capability caches must expire, transient failures must be retryable,
and an ambiguously accepted generation must not be replayed after a crash.

Native subscription authentication stays with the native executable: GinkGo
does not read/copy OAuth tokens, implement refresh, or silently change billing
paths. Codex/Claude generation-only controls remove model-callable action tools;
they are not a sandbox around native authentication or mandatory organization
policy. Managed startup hooks, policy loading, metadata traffic, and disk writes
can still occur. Reject actual incompatible tool/MCP requirements rather than
account plans, and describe native-policy effects separately from model tools.

## AI write boundary

`Provider.Complete` returns untrusted output. The service selects namespace-bound
context, validates a structured draft, and commits via the notes service. The
model may select only an eligible target in the requested namespace; application
code assigns provenance and timestamps. Expansions use compare-and-swap and
append without retries on conflict. Error handling follows the
[fallback contract](ai.md#fallbacks-and-errors). Returned reasons use fixed
application messages to avoid exposing provider diagnostics.

Provider-side schemas only improve generation. They never replace GinkGo's
strict proposal validation, namespace/target checks, byte budgets, tag policy,
or compare-and-swap boundary. Screenshot capture, image relevance instructions,
original text and image retention, attachment storage, and sync remain GinkGo
responsibilities. Historical attachments are not resent with candidate notes.

Drafts require a nonempty final normalized tag set. Creation merges defaults,
explicit tags, and draft tags; expansion merges target and explicit tags with
draft tags. Trimming, lowercase conversion, blank removal, and deduplication
preserve merge order. Invalid drafts still fail even when existing tags make
the final set nonempty. Vocabulary hints, when supported, use only names from
the exact namespace; they do not broaden body retrieval. See
[`internal/ai`](../internal/ai) and its tests for the structured API and limits.

## Capture and native trust

Raw audio is bounded and short-lived. Transcription revisions replace prior text.
Capture cancellation discards a partial note selection, while graceful stop may finalize eligible
audio. Joins and cleanup must not race native destruction or allow a canceled
selection to borrow later ambient speech.

The Sherpa helper runs with the daemon's privileges and inherits its environment,
including any provider credentials. Keep secrets out of argv, protocol fields,
and diagnostics. The native KWS adapter is pinned to a reviewed
hash-bound profile and owned integer emission clock. KWS provides an emission
grid; physical word boundaries remain unknown. Its clock/reset/duplicate checks and profile
guard are required for recording authority. A stock C API timestamp or a
successful CUDA execution-provider initialization cannot replace these checks.
See [`native_profile.go`](../internal/sherpavoice/native_profile.go) and
[`native_kws.go`](../internal/sherpavoice/native_kws.go) for exact profile and
ABI details.

Selected audio includes the wake, retained context, and dictation. The parent
validates each `note_candidate`, rebinds engine metadata, and checks save opt-in
before persistence. Candidate identity, text, and span requirements are specified
in the [private helper protocol](voice-protocol.md#private-helper-stdio-v1).
