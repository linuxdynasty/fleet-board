# 02j. Hub runtime: listeners, configuration, state directory, commands

- Status: Accepted; contracts unfrozen until v0.1 (ADR 0019)
- Date: 2026-10-02
- Part of: [02-contracts.md](02-contracts.md), [02f](02f-hub-authentication.md)
- Decisions: [ADR 0022](../adr/0022-drift-audit-decisions.md) (supersedes the
  "one TLS port" sentence of [ADR 0004](../adr/0004-agent-connectivity.md))
- Values: [defaults.md](defaults.md)

The other specs say what `fleetd` does. This one says how an owner starts it,
what it listens on, where its files are and where its settings live. Nothing here is
on the wire; none of it is in a proto.

## Listeners

`fleetd run` opens up to four listeners in one process.

| Listener | Key | Default | Serves | Who may connect |
|---|---|---|---|---|
| Agent | `listen.agent` | `0.0.0.0:7443`, TLS 1.3 | `AgentService` only: `Enroll`, `Connect`, `RenewCert`, `SessionLog` | `Enroll`: any peer that holds a token (server-authenticated TLS, [02g](02g-agent-security.md)); every other RPC: a client certificate signed by the hub CA, checked against the revocation list |
| API | `listen.api` | `local_trusted`: `127.0.0.1:7080`, plain HTTP. `authenticated`: no default, must be set; HTTPS only | the Connect API (`FleetService`, `Watch`), the WebSocket attach, the embedded dashboard, `/auth/github/*` | by mode, [02f](02f-hub-authentication.md) |
| Metrics (optional) | `listen.metrics`, `--metrics-addr` | none: `/metrics` is served on the API listener | `/metrics` | loopback peer, or an `ADMIN` API token ([05b](05b-observability.md)) |
| pprof (optional) | `listen.pprof`, `--pprof-addr` | none | `/debug/pprof` | loopback listener only, plus `ADMIN` |

The two main listeners are separate because they have different callers: agents
on other machines must reach the hub over mTLS, while `local_trusted` binds the
API to loopback and `fleetd` refuses to start with any other API address in that
mode ([02f](02f-hub-authentication.md#modes)). The loopback rule is about
`listen.api` only; the agent listener is always reachable by the machines that
enroll. ADR 0004's "one TLS port" therefore means the agent port: 7443 stays its
default and the API gets its own.

The agent listener's certificate is issued by the hub CA for the names in
`tls.agent_names` (default: the machine's hostname, `<hostname>.local`,
`localhost`, `127.0.0.1`, `::1`), so an agent on the hub's own machine connects to
`127.0.0.1:7443` and verifies it like any other. In `authenticated` mode the API
certificate is the owner's (`tls.api_cert_file`, `tls.api_key_file`) or a
Tailscale certificate (`tls.api_tailscale: true`); without one `fleetd` refuses
to start.

**Discovery.** While `discovery.mdns` is true (default) `fleetd` advertises
`_fleetd._tcp` on the agent port with `hashicorp/mdns` (ADR 0023). Where no
multicast interface is usable it logs once and the agents use their cached or
configured addresses.

## Configuration file

`fleetd` reads one YAML file at start: `~/.config/fleetd/fleetd.yaml`, or the
path in `--config` or `FLEETD_CONFIG`. It is the owner's file: the hub never
writes it, except that `fleetd init` creates it with the defaults and
`fleetd setup github` adds the `github` keys. Changes need a restart. It holds
what must exist before the first request or must never be reachable through the
API (secrets, bind addresses, certificates). Everything else an owner
configures is hub state set through the API ([Configuring a fresh hub](#configuring-a-fresh-hub)).

```yaml
mode: local_trusted            # local_trusted | authenticated (02f)
state_dir: ~/.local/state/fleetd
listen: {agent: "0.0.0.0:7443", api: "127.0.0.1:7080", metrics: "", pprof: ""}
tls: {agent_names: [], api_cert_file: "", api_key_file: "", api_tailscale: false}
public_hosts: []               # Host allow-list; authenticated mode (02f)
public_origins: []             # Origin allow-list; authenticated mode (02f)
discovery: {mdns: true}
github:                        # absent until `fleetd setup github`; ADR 0005, 0022
  app_id: 0
  installation_id: 0
  private_key_file: ""         # mode 0600, the owner's file; never in state.db
  client_id: ""                # GitHub sign-in only (02f)
  client_secret_file: ""       # mode 0600
timing: {max_sync_lag: 10m, verify_grace: 10m, ready_timeout: 90s, goal_file_timeout: 30s}
rate_limits: {read: 600, write: 120, unauthenticated: 30, metrics: 60}   # per minute
telemetry: {}                  # the keys of 05b section 5
```

Secrets are named by path, never inlined. `timing.*` are constants no proto
carries; [defaults.md](defaults.md) lists their defaults and what else is
tunable. The `github` section creates the `Integration` row at start (the API
has no `CreateIntegration`; `UpdateIntegration` edits its label and tracker
projects).

## State directory

`~/.local/state/fleetd/` (`state_dir`), owned by the user that runs `fleetd`:

| Path | What |
|---|---|
| `state.db`, `-wal`, `-shm` | the hub store, including `hub_identity` (CA key, account salt; [02g](02g-agent-security.md)) |
| `logs.db` | the log store (ADR 0015) |
| `revoked-serials` | the revocation list, kept outside `state.db` so a restore re-applies it (02g) |
| `backups/` | nightly `VACUUM INTO` copies (ADR 0014) |
| `admin.sock` | the local admin socket, mode 0600, for `fleetd token create`, `fleetd user invite` and `fleetd claim` |

The agent's directory is `~/.local/state/fleet-agent/` ([04](04-technology-review.md) Q9).

## Commands

| Command | Does |
|---|---|
| `fleetd run` | starts the hub (launchd or `systemd --user` runs this) |
| `fleetd init` | writes the default configuration file and the state directory |
| `fleetd setup github` | writes the App manifest and the `github` keys ([ADR 0022](../adr/0022-drift-audit-decisions.md)) |
| `fleetd enroll-token`, `claim`, `token create`, `user invite` | admin actions over `admin.sock` ([02f](02f-hub-authentication.md)) |
| `fleetd backup`, `restore`, `debug agent-stream`, `insights rebuild` | local, never RPCs ([02b](02b-hub-api.md#local-commands)) |

## Configuring a fresh hub

A hub starts empty and read-only in effect: with `auto_dispatch` off
([defaults.md](defaults.md)) it changes nothing on any machine. The owner
configures it through the API, from the dashboard Settings view or the CLI;
the same calls are in [02b](02b-hub-api.md), and all need `SETTINGS` or `ADMIN`
(`local_trusted`: the local owner):

1. Enroll machines: `fleetd enroll-token` or `CreateEnrollmentToken` ([02g](02g-agent-security.md)).
2. Connect GitHub: the `github` keys above; the hub then lists the integration.
3. Name the watched repos: `CreateRepo` and `UpdateRepo` (consent, caps, hooks mode).
4. Set accounts: `UpdateAccount` (reserve, consent, mode); map a Codex account that
   shows no identity with `spec.bindings` ([03b](03b-usage-and-sync.md#accounts-discovered-mapped-created-removed)).
5. Fleet and group limits: `UpdateSettings`.
6. Or all at once: `ExportConfig` and `ImportConfig` with a `fleet.yaml`
   ([02b](02b-hub-api.md#config-export-and-import)).
