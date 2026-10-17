# Configuration

## File, paths, and precedence

Configuration resolves in this order: built-in defaults, file, then `GINKGO_*`
environment variables. Supported command flags override settings. The automatic
search order is `$XDG_CONFIG_HOME/ginkgo`, `~/.config/ginkgo`, then the current
directory, looking for `config` with a supported extension. `--config` selects a
specific file. Generated configuration uses TOML; YAML can be loaded with
`--config`.

```sh
ginkgo-cli config generate
ginkgo-cli --config /absolute/path/config.toml config check
```

Generation writes `$XDG_CONFIG_HOME/ginkgo/config.toml` or
`~/.config/ginkgo/config.toml`. It will not replace an existing file without
`--update` (merge missing defaults) or `--overwrite` (replace); either makes a
backup when changing a file. `config check` validates configuration fields,
including required keys and base64 encoding. Connectivity and OS-keyring
availability need separate checks. The loader ignores file-read errors and can report `Config OK`
after loading defaults. CLI startup can create/open the database,
including for config commands.

SQLite is stored at `data_dir/ginkgo.db`, defaulting to `$XDG_DATA_HOME/ginkgo` or
`~/.local/share/ginkgo`. Use an absolute path for a custom `data_dir`.
Environment names replace dots with underscores:

```sh
GINKGO_DATA_DIR="$HOME/.local/share/ginkgo-scratch" ginkgo-cli config check
GINKGO_NAMESPACE=work ginkgo-cli note list
```

Note commands use the daemon's socket and storage. Set the daemon's paths and
namespace where it starts; changing a client environment variable does not
reconfigure a running daemon. Use the same config and runtime paths for the CLI
and daemon.

## Namespace and general settings

```toml
namespace = "default"
default_tags = ["journal"]
http_addr = "127.0.0.1:7465"

[export]
page_size = 200
```

The namespace defaults to `default`; initialize additional namespaces with
`ginkgo-cli config namespace init -n work`. Set `default_tags` for new notes. The
`export.page_size` controls [list and search paging](usage.md#list-and-search).
Reminder settings are reserved; scheduling is unimplemented.

Namespace initialization asks about encryption and key storage. Before sharing
a namespace, follow [sync key setup](sync.md#encryption-keys). Configure optional
providers and audio backends in the [AI](ai.md) and [voice](voice.md) guides.

For standard voice setup, run `ginkgo-cli voice setup`, then set `[voice]`
`enabled = true` and `mode = "raw"` (or `"ai"`). Generated configuration keeps
these common controls prominent. Advanced voice settings use built-in defaults
and automatic discovery; all existing keys and environment overrides remain
supported. `config generate --update` preserves advanced values already present
without adding the advanced settings to a basic config.

## Local daemon listener

Besides its Unix IPC socket, the daemon starts an unauthenticated, health-only
`/healthz` HTTP listener. `http_addr` defaults to `:8080`, which binds all
interfaces. Use a free loopback address, as in the example above, at the top level
of TOML before any table.

For a foreground daemon, `GINKGO_HTTP_ADDR=127.0.0.1:7465 ginkgo-cli daemon` is an
alternative. Services need the value in their loaded config and a restart; a
foreground environment override does not affect an existing service. An occupied
port prevents normal startup. The separate server process also uses `http_addr`;
give it a separate config and data directory, and see [server setup](sync.md#server-setup).

If a note command cannot connect, check that the daemon is running and that both
processes use the same config and runtime directory. Restart the daemon after
configuration changes; already running processes keep their loaded settings.
