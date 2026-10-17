# Sync between devices

Local edits are recorded in an append-only event log. The daemon syncs enabled
remotes in the background, tracks separate push/pull cursors in `data_dir/sync/`,
and retries failures with backoff. Notes remain local and writable when a remote
is unavailable.

## Configure and run

Add the remote to the daemon's configuration, then restart the daemon:

```toml
[remotes.origin]
enabled = true
url = "https://sync.example.com"
token = "replace-with-server-token"
```

With the daemon running:

```sh
ginkgo-cli note queue --limit 10
ginkgo-cli note sync
```

`note queue` lists pending events per enabled remote. `note sync` acknowledges a
requested sync pass; check the remote client for delivery. Background defaults
are `sync.interval = "60s"`, `sync.max_backoff = "10m"`, and `sync.batch_size = 256`.

## Server setup

Run `ginkgo-cli server` with a separate config, data directory, and listener port
from the [local daemon](config.md#local-daemon-listener):

```toml
data_dir = "/absolute/path/to/replication-data"
http_addr = "127.0.0.1:8080"

[auth]
token = "replace-with-a-strong-shared-token"
```

```sh
ginkgo-cli server --config /absolute/path/server.toml
```

`server --listen 127.0.0.1:8081` overrides the server's listener. Clients use the
same token in `remotes.<name>.token`. Put the HTTP server behind a TLS-terminating
proxy for non-local connections. The bearer token grants access to the full event
log across namespaces.

<details>
<summary>Run the server in Docker</summary>

From the checkout, build the image and provide the token through the environment:

```sh
docker build -t ginkgo-server .
docker run --rm -p 127.0.0.1:8080:8080 \
  -e GINKGO_AUTH_TOKEN -v ginkgo-data:/data ginkgo-server
```

Set `GINKGO_AUTH_TOKEN` securely before running the command. The named volume
stores the server database and cursors under `/data`.

</details>

## Encryption keys

The server stores and relays opaque payload bytes. `plain_v1` is unencrypted JSON;
`enc_v1` uses XChaCha20-Poly1305. E2EE encrypts replicated payloads. Event metadata,
including namespace, ID, timestamp, type, signer ID,
and origin label, remains visible. Previously uploaded plaintext events are not
encrypted retroactively.

For ordinary bidirectional E2EE sync, every participating client, including the
originating device, must have the same 32-byte secret as both `read_key` and
`write_key`. Clients pull their own events. `config namespace init` generates
these keys independently, so that generated pair cannot be copied as-is for a
shared bidirectional namespace. Set the matching secret before writing or syncing.
For file-backed keys, privately edit each client's TOML. Replace both placeholders
with the same base64 encoding of a cryptographically random 32-byte secret:

```toml
[namespaces.work]
e2ee = true
key_provider = "config"
read_key = "<base64-shared-secret>"
write_key = "<base64-shared-secret>"
```

With `key_provider = "system"`, that secret must occupy both read and write slots
in each device's OS keyring. Key IDs do not transfer keys; the CLI has no key-import
command. `config namespace key -n work` prints encryption keys, so keep its output
out of logs. Restart each daemon after editing its configuration.

Changing keys does not migrate encrypted history or re-encrypt uploaded events.
Retain old keys and back up data and configuration before planning a migration.
Keep encryption and signing private keys on clients, and keep all secrets out of
logs, shell history, and bug reports.

## Signatures

Clients can sign event metadata and payloads with Ed25519. The server requires a
trusted signature only when that namespace has a nonempty `trusted_signers` list
of base64 public keys. On the server:

```toml
[namespaces.work]
trusted_signers = ["<base64-public-key>"]
```

During namespace initialization, choose config-backed signing keys to store
`signer_pub` and `signer_priv` with `signer_key_provider = "config"`. Copy each
client's `signer_pub` into the server's trust list. Use a different signing key
per device.

For keyring-backed signing, record the public key shown during initialization;
GinkGo's key command only retrieves encryption keys. An existing signer public
key must be retrieved through the OS keyring's tools. Pull clients currently skip
signature verification; server enforcement is the authenticity check.

## Conflicts and writer compatibility

Local edits use compare-and-swap versions. Replication applies snapshots without
distributed strong consistency, automatic text merging, or a conflict-resolution
interface. Divergent captured-original histories require manual reconciliation.
Every writing device must use a provenance-aware
version. Older writers can discard original-source history when they reserialize
an entry. The storage and compatibility contract is documented in the
[developer internals guide](internals.md#note-provenance).
