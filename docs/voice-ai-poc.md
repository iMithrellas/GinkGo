# Optional AI notes and voice POC

**Status: provisional.** This describes the intended POC, not shipped behavior. Treat
the scope and interfaces as unsettled until implementation commits and tests verify
them. A runnable quickstart will be added after integration; this document does not
specify final configuration keys or command flags.

## POC priorities

1. **Daemon-owned AI note workflow.** Add `ai note --stdin` with explicit new-note
   (`--new`) and expansion (`--note`) modes. The CLI supplies input and displays the
   result; the daemon owns provider calls, bounded namespace-scoped context retrieval,
   action validation, persistence, and sync. Creation uses the existing note path;
   expansion uses the exact observed version for CAS. A conflict, invalid result, or
   disabled/unavailable AI must not overwrite or lose the user's input: save it as a
   raw fallback note and report why AI handling was not applied.
2. **Separate original-input provenance.** Store each contributing typed input or
   transcript as typed provenance, separate from the rendered/indexed note body.
   Expansion appends a source and preserves earlier sources. `--original` explicitly
   exposes originals; ordinary rendering, context, and FTS do not. Provenance must
   survive edits, CAS, import/export, event snapshots, and encrypted sync. Existing
   entries need a safe baseline before their first AI rewrite. Do not ship provenance
   writes until old-writer behavior is addressed: unsupported writers must not be
   allowed to erase metadata on edit (a minimum-version/capability policy remains to
   be decided).
3. **OpenCode V2 provider first.** Use the V2 CLI through a verified generation-only
   execution path: disable coding tools and unrelated hooks/integrations using
   controls verified for the supported version. Never treat a filesystem read-only
   sandbox alone as disabling command execution. Normalize provider output into a
   typed draft/action and validate it in Go before any write. Add a generic fixed
   executable/wrapper or OpenAI-compatible HTTP adapter only where a usable boundary
   already exists; do not expand the POC into multiple provider-specific agents.
4. **Local raw WAV transcription.** Accept a bounded local WAV input and invoke
   on-demand `whisper-cli` with an already-installed model. Save the transcript as a
   normal note with its original transcript recorded separately. This path must work
   without AI enabled or an AI provider installed. Missing executable/model or
   transcription failure is explicit; never download assets automatically.

## Safety and privacy invariants

- No model output directly chooses shell commands, SQL, executable paths, or arbitrary
  IPC. Validate operation, note ID, namespace, fields, and context limits in Go.
- Reads and context candidates are strictly namespace-scoped. An explicit expansion
  target must be in scope. Never silently retry a failed CAS against a newer version.
- Bound context, input, output, execution time, and WAV size/duration. Treat note text
  as untrusted data, not instructions. Keep the original input independent of provider
  output and preserve it on every fallback.
- Raw note creation and transcription do not depend on AI initialization or health
  checks. Disabled/unavailable/invalid AI falls back to a new raw note; failed
  expansion must not mutate its target.
- A local CLI can still send selected input and retrieved notes to a remote model.
  Sync E2EE does not protect this separate provider request. Make remote disclosure
  explicit; send only bounded, relevant context, and do not launch providers when AI
  is disabled.
- Preserve ordinary note behavior, existing IDs/namespaces, CAS semantics, and sync.
  Do not claim compatibility with older writers until provenance-preservation policy
  is implemented and tested.

## Verification gates

- Use fake providers and executable fixtures in normal tests; no paid API, model
  download, microphone, or real audio is required for the test suite.
- Verify no provider launch when disabled; raw fallback for unavailable providers,
  malformed/invalid actions, and conflicts; bounded namespace-scoped context; and that
  CAS conflicts never overwrite concurrent edits.
- Test provenance migration and preservation through edits, import/export, event/hash
  behavior, and encrypted multi-client sync. Confirm originals are absent from normal
  rendering/FTS and available only through explicit `--original` display.
- Verify OpenCode V2 generation-only controls and cancellation against the supported
  CLI version; use fixture-based output tests for parsing and invalid responses.
- Verify WAV validation, missing `whisper-cli`/model behavior, cancellation, raw note
  creation, and separate transcript provenance with a fake executable. Keep
  hardware/audio quality tests opt-in.

## Deferred beyond this POC

- Persistent or idle-timeout STT residency; model catalog, downloads, prefetch, and
  model management.
- Microphone capture, VAD, lifecycle hooks, managed visualizer/companion processes,
  and desktop integration.
- Wake-word detection/listening, spoken intent routing, and TTS.
- Codex and Claude Code adapters or a broad provider/plugin framework.
- AI search, summaries, embeddings, relevance ranking, and other semantic retrieval.

These are later milestones, not implied POC commitments. Revisit them only after the
relevant capability is implemented, tested, and documented; add the runnable quickstart
then, based on actual supported flags and configuration rather than proposals.
