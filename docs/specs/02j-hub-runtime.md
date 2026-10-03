# 02j. Hub runtime: listeners, configuration, state directory, commands

- Status: Accepted; contracts unfrozen until v0.1 (ADR 0019)
- Date: 2026-10-02
- Part of: [02-contracts.md](02-contracts.md), [02f](02f-hub-authentication.md)
- Decisions: [ADR 0022](../adr/0022-drift-audit-decisions.md) (supersedes the
  "one TLS port" sentence of [ADR 0004](../adr/0004-agent-connectivity.md)),
  [ADR 0024](../adr/0024-pre-build-owner-decisions.md) (decisions 11 to 14)
- Values: [defaults.md](defaults.md)

The other specs say what `fleetd` does. This one says how an owner starts it,
what it listens on, where its files are and where its settings live. Nothing here is
on the wire; none of it is in a proto.

## Listeners

`fleetd run` opens up to four listeners in one process.

| Listener | Key | Default | Serves | Who may connect |
|---|---|---|---|---|
| Agent | `listen.agent` | `0.0.0.0:7443`, TLS 1.3 | `AgentService` only: `Enroll`, `Connect`, `Terminal`, `SessionLog`, `RenewCert` | `Enroll`: any peer that holds a token (server-authenticated TLS, [02g](02g-agent-security.md)); every other RPC: a client certificate signed by the hub CA, checked against the revocation list |
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

**Using v1 from another machine.** In v1 the dashboard and `fleet` run on the hub
machine only (decision 11 of ADR 0024); sign-in arrives in v3. From another
machine, forward the port over SSH and use the same port number on both sides:

```
ssh -L 7080:127.0.0.1:7080 <hub>      # then open http://localhost:7080, or run fleet there
```

The hub accepts the `Host` names `localhost` and `127.0.0.1` with its listening
port ([02f](02f-hub-authentication.md)); a forward to a different local port sends
a `Host` it refuses (`HOST_NOT_ALLOWED`). `fleet` on the other machine sets
`FLEET_URL=http://localhost:7080`.

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
| `fleetd run` | starts the hub (launchd or `systemd --user` runs this; see [staying awake](#staying-awake-and-surviving-logout)) |
| `fleetd upgrade` | fetches the next GitHub release, verifies its checksum and swaps the binary ([install and upgrade](#install-and-upgrade)) |
| `fleetd init` | writes the default configuration file and the state directory |
| `fleetd setup github` | writes the App manifest and the `github` keys ([ADR 0022](../adr/0022-drift-audit-decisions.md)) |
| `fleetd enroll-token`, `claim`, `token create`, `user invite` | admin actions over `admin.sock` ([02f](02f-hub-authentication.md)) |
| `fleetd backup`, `restore`, `debug agent-stream`, `insights rebuild` | local, never RPCs ([02b](02b-hub-api.md#local-commands)) |

## Staying awake and surviving logout

The hub is a laptop in the author's setup, and a sleeping hub stops the GitHub
sync, the scheduler, the System signals and the nightly backup (decisions 12 and
13 of ADR 0024).

- **Hub on macOS.** The launchd unit runs `fleetd` under `caffeinate -s`
  (`ProgramArguments`: `/usr/bin/caffeinate`, `-s`, the `fleetd` path, `run`), or
  the equivalent on another OS (`systemd-inhibit --what=sleep`). `-s` keeps the
  machine awake only while it is on power; on battery the laptop can still sleep,
  and the agents reconnect when it wakes.
- **macOS workers.** A LaunchAgent starts at the owner's login and needs the owner
  logged in (no auto-login is set up; a locked screen is fine).
- **Linux workers.** A `systemd --user` service stops when the owner's last session
  ends unless lingering is on. The owner runs `sudo loginctl enable-linger <user>`
  once per worker (one admin step, the only one fleet-board asks for);
  `fleet-agent doctor` runs `loginctl show-user <user> --property=Linger` and says
  how to fix it.

## Install and upgrade

Decision 14 of ADR 0024, built in plan PR 16.

- **Where binaries live.** GitHub Releases of this repository: one archive per
  program and platform (darwin and linux, amd64 and arm64) and one `SHA256SUMS`
  file. Nothing else is published.
- **Install.** A one-line install script (the "Add machine" command shows it) downloads
  the archive for the machine, checks it against `SHA256SUMS`, installs it into the
  owner's `~/.local/bin` and installs the service unit. A checksum mismatch installs
  nothing.
- **Upgrade.** `fleet-agent upgrade` and `fleetd upgrade` run on the machine, as the
  owner. Each fetches the latest release (or the one named with `--version`), checks
  the checksum, replaces its own binary, and restarts the service. The databases
  migrate forward only when the new binary starts. An upgrade is never a hub command
  (01, M). A hub and its agents are upgraded one at a time; the wire contract is
  additive-only from `v0.1`.
- **macOS signing.** The binaries are not signed or notarized for now. macOS marks a
  downloaded file as quarantined, so the owner clears the flag once per machine with
  `xattr -d com.apple.quarantine <binary>`; the install script prints the command.

## `fleetd setup github`

The owner runs it once on the hub host. It uses GitHub's App manifest flow, so
nothing is typed into a form by hand and no secret passes through a terminal
line the owner pastes elsewhere.

1. `fleetd setup github` binds a one-shot HTTP listener on `127.0.0.1` at a random
   port (never on another address) and prints a local URL. The page behind it
   posts the manifest to GitHub: the App's name, `url`, `redirect_url` (that
   listener), `public: false`, `default_events: []` (no webhook), and the
   permission set of [ADR 0022](../adr/0022-drift-audit-decisions.md) (read-only,
   repository permissions only; the user authorization settings, with callback
   `<public origin>/auth/github/callback`, only in `authenticated` mode with
   GitHub sign-in on, never in v1).
2. The owner confirms the App on GitHub. GitHub redirects to the listener with a
   one-time `code` and the `state` the command generated; a wrong `state` is
   refused. The listener serves one redirect and closes.
3. The command exchanges the code once (`POST /app-manifests/{code}/conversions`),
   which returns the App id, the **private key** and, when sign-in was asked for,
   the client id and secret.
4. It writes the private key to `github-app.pem` next to `fleetd.yaml` (mode
   0600, created exclusively, never logged, never echoed) and the client secret
   to `github-client-secret` the same way, then adds `app_id`,
   `private_key_file` (and `client_id`, `client_secret_file`) to the `github`
   section of `fleetd.yaml`. Nothing secret goes into `state.db`.
5. The owner installs the App on the watched repos (plan 0.2). Re-running the
   command signs an App token and lists `GET /app/installations`; with exactly one
   installation it writes `installation_id`, with several it asks which, with
   none it says to install the App first.
6. It prints "restart `fleetd` to apply" (the file is read at start, above). After
   the restart the hub creates the `Integration` row from the `github` keys.

The command is tested against recorded GitHub responses (plan PR 13a), including
a wrong `state`, a used code, and the file modes.

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
