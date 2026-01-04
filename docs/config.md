# Configuration

GinkGo reads configuration via Viper, supporting YAML/TOML files and `GINKGO_*` environment variables.

## Search Paths
- `$XDG_CONFIG_HOME/ginkgo/config.yaml`
- `~/.config/ginkgo/config.yaml`
- `./config.yaml`

Override with `--config /path/to/config.yaml`.

## Example (YAML)
```yaml
data_dir: ~/.local/share/ginkgo
db_url: sqlite://~/.local/share/ginkgo/ginkgo.db
namespace: default
remotes:
  origin: https://ginkgo.example.com
notifications:
  enabled: true
  every_days: 3
```

## Namespace Configuration
Namespace settings live under `namespaces.<name>`. They control E2EE, key storage, and signing.

```toml
[namespaces.work]
e2ee = true
key_provider = "system"   # or "config"
key_id = "ginkgo/ns/work" # used when key_provider = "system"
read_key = "..."          # base64 when key_provider = "config"
write_key = "..."         # base64 when key_provider = "config"

signer_key_provider = "system" # or "config"
signer_key_id = "ginkgo/signer/laptop"
signer_pub = "..."             # base64 when signer_key_provider = "config"
signer_priv = "..."            # base64 when signer_key_provider = "config"
origin_label = "mithrel-laptop"

# Replication server trust list (base64 Ed25519 public keys).
trusted_signers = ["..."]
```

Notes:
- `e2ee = true` encrypts replication payloads; local storage remains plaintext.
- `key_provider = "system"` uses the OS keyring; `config` stores keys in config files.
- `trusted_signers` is used by replication servers to validate incoming signatures.
