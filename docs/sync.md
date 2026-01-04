# Replication & Offline Buffer

GinkGo uses an event log to record immutable changes. The same log supports offline operation and background replication when connectivity returns.

## Model
- Append-only event log with entry upserts and deletes.
- Local outbox queues outgoing events per-remote.
- CAS updates (compare-and-swap) enforce strong consistency.

### Payload-Based Events
Replication sends a lightweight event envelope plus an opaque payload. The server never interprets payload contents; clients encode and decode them locally. This keeps the server simple and allows encrypted payloads without changing the server protocol.

Payload types:
- `plain_v1`: JSON-encoded note upsert/delete payloads.
- `enc_v1`: encrypted payloads (see E2EE).

### Signatures
Clients can attach Ed25519 signatures over a canonical, length-prefixed payload that includes event metadata and the payload bytes. Servers can optionally enforce signatures using a configured trusted signer list.

### E2EE
When `namespaces.<name>.e2ee = true`, clients encrypt payloads before replication and decrypt on pull. Local storage stays plaintext for search/indexing. Encrypted payloads include an algorithm tag and nonce and are opaque to the server.

## Flow
1. Write locally to the log.
2. Push batches to remotes when available.
3. On conflicts, refuse and surface to the user for manual resolution.

## Daemon vs CLI
The daemon handles background sync; the CLI can trigger `ginkgo-cli sync` for foreground runs.
