# Replication & Offline Buffer

GinkGo uses an event log to record immutable changes. The same log supports offline operation and background replication when connectivity returns.

## Model
- Append-only event log with entry upserts and deletes.
- Local outbox queues outgoing events per-remote.
- CAS updates (compare-and-swap) enforce strong consistency.

## Flow
1. Write locally to the log.
2. Push batches to remotes when available.
3. On conflicts, refuse and surface to the user for manual resolution.

## Daemon vs CLI
The daemon handles background sync; the CLI can trigger `ginkgo-cli sync` for foreground runs.
