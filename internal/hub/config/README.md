# internal/hub/config

Loads and checks `fleetd.yaml`, the owner's configuration file
([02j](../../../docs/specs/02j-hub-runtime.md#configuration-file)).

```go
path, err := config.Path(flag, os.Getenv) // --config, $FLEETD_CONFIG, ~/.config/fleetd/fleetd.yaml
cfg, err := config.Load(path)             // strict; refusals are *config.Error
err = config.WriteDefault(path)           // fleetd init: the defaults, mode 0600, never replaces a file
```

- **Strict.** An unknown key, a second YAML document or a value of the wrong
  type is refused. Keys not written keep the 02j defaults (`Default()`).
- **Durations** are strings (`10m`, `90s`); a bare number or a value of zero or
  less is refused under the key's name.
- **Loopback only.** In `local_trusted` mode `listen.api` (and `listen.metrics`
  when set) must be exactly `127.0.0.1` or `::1`. `localhost`, `0.0.0.0`, `::`,
  another loopback address and LAN addresses are refused; there is no override.
- **Not built yet.** `mode: authenticated`, `listen.pprof`, `public_hosts`,
  `public_origins`, `tls.api_cert_file`, `tls.api_key_file`, `tls.api_tailscale`
  and `telemetry.otlp.endpoint` are refused (`Error.NotBuilt`) until their plan
  rows land, never accepted and ignored. `discovery.mdns`, `timing`,
  `rate_limits` and `github` load for the rows that use them.
- **Admin socket path.** `state_dir` is refused when `<state_dir>/admin.sock`
  would not fit in `sun_path` (104 bytes on darwin, 108 on Linux, counting the
  closing NUL). `AdminSocketPath` names it.
- **`github`** is checked for shape only; the key files are never opened here.

Tests: `CGO_ENABLED=0 go test ./internal/hub/config/`.
`TestEveryDefaultMatches02j` compares every default with values written out by
hand from 02j and defaults.md.
