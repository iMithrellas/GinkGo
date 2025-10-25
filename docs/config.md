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
