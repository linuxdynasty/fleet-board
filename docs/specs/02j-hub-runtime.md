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
`localhost`, `127.0.0.1`, `::1`) and always the mDNS name
`fleetd-<hostname>.local` (see Discovery, below), so an agent on the hub's own
machine connects to `127.0.0.1:7443` and verifies it like any other. In `authenticated` mode the API
certificate is the owner's (`tls.api_cert_file`, `tls.api_key_file`) or a
Tailscale certificate (`tls.api_tailscale: true`); without one `fleetd` refuses
to start.

**Discovery.** While `discovery.mdns` is true (default) `fleetd` advertises
`_fleetd._tcp` on the agent port with `hashicorp/mdns` (ADR 0023). The SRV target
is `fleetd-<host>.local`, always in the server certificate's names; the hub
advertises its explicit non-loopback addresses, never the OS host name; TXT
carries the proto range only. Advertising is best effort and never stops the hub
([ADR 0028](../adr/0028-mdns-best-effort.md)): a host with no address to
advertise, no multicast family that binds, a responder that cannot start, or a
served certificate that lacks the target logs once (`discovery.advertise_refused`
or `discovery.advertise_failed`, [05b](05b-observability.md)) and the agents use
their cached or configured addresses. A family that cannot bind is logged once
and the other still advertises. The host name is the hub's own, lower-cased and
without `.local`, the one the default certificate names use; a host name that
makes no DNS name advertises nothing.

The agent keeps one address per IP family from an answer, so the hub advertises
the addresses of one interface, at most one IPv4 and one IPv6. For a listener on
the wildcard address it is, in this order: the interface that holds the default
route's source address, when it is up, multicast, not point-to-point and not named
like a tunnel (a Linux host that bridges its NIC holds its address and the route
on `bridge0`, which counts); else the first interface that is not named like a
bridge, container, VM or tunnel; else a tunnel-named route holder that is not
point-to-point. A point-to-point interface is never used. A listener bound to one
address advertises that address on the interface that holds it. An IPv4
link-local address (169.254/16) is advertised only when nothing else qualifies;
IPv6 link-local never.

The address list is read at start; after a network change restart `fleetd` or
give the agent the new address (re-advertising on a network change is later
work). So a hub found "within one 60 s backoff step" of moving
([ADR 0028](../adr/0028-mdns-best-effort.md) decision 2) is true only after that
restart.

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

The file is read strictly: an unknown key, a second YAML document or a value of
the wrong type refuses the start, naming the key. A key whose feature this build
does not have yet is either refused as "not built yet" or loaded with no effect
until the plan row that uses it; [02k](02k-v1-surface-and-cli.md#out-of-release-behaviour)
lists which, and nothing is silently ignored. Keys not written keep the defaults
above; a key written with no value (empty, `~` or `null`) is refused, naming it,
since it is written but holds nothing (in YAML `~` is no value, not the home
folder). A list item or a key name with no value is refused the same way. Durations are strings (`10m`, `90s`). `state_dir`
is refused when `<state_dir>/admin.sock` would not fit in a Unix socket path (104
bytes on darwin, 108 on Linux).

Secrets are named by path, never inlined. `timing.*` are constants no proto
carries; [defaults.md](defaults.md) lists their defaults and what else is
tunable. The `github` section creates the `Integration` row at start, from plan
row 11 (the API
has no `CreateIntegration`; `UpdateIntegration` edits its label and tracker
projects).

## State directory

`~/.local/state/fleetd/` (`state_dir`), owned by the user that runs `fleetd`,
mode 0700. `state.db` is mode 0600. `fleetd run` refuses to start when the folder
or `state.db` is not owned by that user or is open to the group or others, like
ssh with a loose key; it never changes a mode itself.

| Path | What |
|---|---|
| `fleetd.lock` | an exclusive lock (`flock`) that `fleetd init`, `fleetd run` and `fleetd restore` take before they open `state.db`, so a second `fleetd` on the same folder exits at once (#92). The kernel drops it when the process dies, so a killed hub never blocks the next start. `fleetd backup` takes no lock, neither this one nor a shared one: it only reads `state.db` on a read-only connection of its own, so it works whether or not `fleetd run` holds the lock |
| `state.db`, `-wal`, `-shm` | the hub store, including `hub_identity` (CA key, account salt; [02g](02g-agent-security.md)) |
| `state.db.pre-restore-<UTC stamp>` | the store a `fleetd restore` replaced, checkpointed (its WAL folded in) and kept as a hard link, mode 0600, with `-2`, `-3` when two restores share a second. It holds the CA key like a backup; nothing removes it, so delete it once the restored hub works. A restore killed before its rename leaves `.state.db.restore-*` (mode 0600), which `fleetd init`, `run` and `restore` remove at start under `fleetd.lock` |
| `logs.db` | the log store (ADR 0015) |
| `revoked-serials` | the revocation list, mode 0600, kept outside `state.db` so a restore re-applies it; format, write order and the start check in [02g](02g-agent-security.md#hub-identity-and-restore) |
| `backups/` | mode 0700, made by the first backup. `VACUUM INTO` copies of `state.db` (ADR 0014), each mode 0600: `nightly-<UTC stamp>.db` from `fleetd run`, `manual-<UTC stamp>.db` from `fleetd backup` and `manual-<UTC stamp>.db.age` from `fleetd backup --encrypt`; the stamp is the UTC time to the second, as in `nightly-20261008T030000Z.db`. `fleetd run` checks every minute and makes a nightly copy at the first check of each local date at or after 03:00 by the wall clock (on a night that skips 03:00, the first check after the jump; on one that repeats it, the first 03:00), and at start or wake when the newest nightly copy is more than 24 h old or there is none; a copy stamped more than 10 minutes past the clock does not count as the newest (`backup.stamp_ahead`, [05b](05b-observability.md)); it keeps the last 7 nightly copies and never removes a manual one. Each copy is checked (it opens, passes `quick_check`, has a known schema version and holds `hub_identity`) before it gets its name. `BACKUP_AGE` reads the newest file of either kind ([05b](05b-observability.md)). With the hub stopped, the copy's read-only connection leaves SQLite's empty `state.db-wal` and `state.db-shm` (mode 0600), which the next `fleetd run` uses. A backup killed mid-copy leaves `backups/.partial-*` or, for an encrypted copy, `<state_dir>/.backup-plain-*` (the plaintext, mode 0600); `fleetd init` and `fleetd run` remove them at start under `fleetd.lock` once they are more than an hour old, since `fleetd backup` takes no lock and a younger one may be a copy in progress |
| `admin.sock` | the local admin socket, mode 0600, for `fleetd enroll-token` (and later `fleetd token create`, `fleetd user invite` and `fleetd claim`). It speaks Connect `FleetService` (unary, HTTP/1.1); only `CreateEnrollmentToken` is built so far and every other method answers unimplemented. The kernel's peer user id must equal `fleetd`'s, or the connection is closed unserved ([02f](02f-hub-authentication.md#api-tokens)). `fleetd run` binds it after taking `fleetd.lock`, first removing a socket a killed hub left behind (anything else at that path is refused), and removes it on a clean stop |

The agent's directory is `~/.local/state/fleet-agent/` ([04](04-technology-review.md) Q9).

## Commands

| Command | Does |
|---|---|
| `fleetd run` | starts the hub (launchd or `systemd --user` runs this; see [staying awake](#staying-awake-and-surviving-logout)). It refuses to start when `state.db` is missing (it never creates one), when `state.db` has no `hub_identity` row (it never mints one), or while another `fleetd` holds `fleetd.lock`. SIGTERM stops it cleanly |
| `fleetd upgrade` | fetches the next GitHub release, verifies its checksum and swaps the binary ([install and upgrade](#install-and-upgrade)) |
| `fleetd init` | writes the default configuration file (when missing), the state directory, and `state.db` with its whole `hub_identity` row in one atomic step: the store is built in a temporary file in the folder and linked into place (a link, unlike a rename, fails rather than replace an existing `state.db`), so `state.db` never exists without the row. On an existing `state.db` it exits non-zero and changes nothing; there is no `--force`. A temporary file left by a crashed init is removed by the next `fleetd init` or `fleetd run`, under `fleetd.lock` |
| `fleetd setup github` | writes the App manifest and the `github` keys ([ADR 0022](../adr/0022-drift-audit-decisions.md)) |
| `fleetd enroll-token --machine <name>` | asks the running hub over `admin.sock` for an enroll token (`CreateEnrollmentToken`, [02g](02g-agent-security.md)); flags `--expect-host`, `--ttl` (default 15m, at most 24h), `--label` (repeatable) and `--start-drained`. Stdout holds only the token, on one line; stderr shows the expiry, the CA fingerprint and the `fleet-agent enroll --token …` line. With no hub running it exits non-zero with "fleetd is not running" and writes nothing |
| `fleetd backup [--encrypt]` | writes one `manual-<UTC stamp>.db` into `backups/` and prints only its path on stdout. It takes no lock, so it works beside a running hub and with the hub stopped (#92). `--encrypt` writes `manual-<UTC stamp>.db.age` with age under a passphrase typed twice at the terminal ([02g](02g-agent-security.md#hub-identity-and-restore)); a stdin that is not a terminal is refused, and there is no flag, variable or file for the passphrase. Local, never an RPC |
| `fleetd restore <backup> [--address <addr>]` | replaces `state.db` with a backup, offline. It takes `fleetd.lock` for the whole command, so while `fleetd run` holds it the restore exits non-zero with `lock_held` and changes nothing (#92). The steps, the refusals and the kept previous store are in [02g](02g-agent-security.md#hub-identity-and-restore). Local, never an RPC |
| `fleetd claim`, `token create`, `user invite` | admin actions over `admin.sock` ([02f](02f-hub-authentication.md)) |
| `debug agent-stream`, `insights rebuild` | local, never RPCs ([02m](02m-hub-api-actions-and-audit.md#local-commands)) |

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

A hub starts empty and idle: with `auto_dispatch` off
([defaults.md](defaults.md)) it starts no goal by itself on any machine. The owner
can still start a goal or a session by hand. The owner
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
   ([02m](02m-hub-api-actions-and-audit.md#config-export-and-import)).
