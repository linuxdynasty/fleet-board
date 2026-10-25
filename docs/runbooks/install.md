# Install and upgrade fleet-board

How to put the hub and the agents on machines, join them into a fleet, and
keep them updated. Read this top to bottom the first time; later, jump to
the step you need.

Words used here: the **hub** is the Mac (or Linux box) that runs `fleetd`
and holds everything. A **worker** is any machine that runs `fleet-agent`,
including the hub itself. A **release** is a set of ready-built programs on
the project's GitHub Releases page.

## What you need

- On every machine: `curl` and `tar`, plus a checksum tool:
  `shasum` (ships with macOS) or `sha256sum` (Linux coreutils, on by
  default in most distros). install.sh checks the download against the
  release checksum before it installs anything. macOS and Linux only, on
  an Intel/AMD or ARM chip.
- The hub needs its dashboard built into `fleetd` (releases already have
  it; see [Staying current](#staying-current)).
- One GitHub App install for the watched repos (plan item 0.2, the
  `fleetd setup github` step). That is separate from installing programs.

## Install the hub (Mac)

On the machine that will be the hub, as the owner (the user who stays
logged in):

```sh
curl -fsSL https://raw.githubusercontent.com/linuxdynasty/fleet-board/main/packaging/install.sh |
  sh -s -- --program fleetd
```

This downloads the hub program for your Mac, checks its checksum (a
fingerprint that proves the download is exactly what the release
published), puts it in `~/.local/bin/fleetd`, and installs a background
service that starts it at login. A checksum mismatch installs nothing.

macOS marks every download as quarantined (a safety flag). The script
clears it and prints the command it ran:

```sh
xattr -d com.apple.quarantine ~/.local/bin/fleetd
```

Then create the hub's config and store, once:

```sh
fleetd init
```

`init` writes the default settings file (`~/.config/fleetd/fleetd.yaml`),
the state folder (`~/.local/state/fleetd/`), and the database with the
hub's identity, all in one step. It never replaces an existing store: run
it once per hub. Start the hub:

```sh
fleetd run
```

or let the background service do it (it runs `fleetd run` for you at
login). The service keeps the Mac awake while on power by running under
`caffeinate -s` (a macOS tool that blocks sleep). On battery the laptop
can still sleep; the agents reconnect when it wakes.

Check it: open `http://localhost:7080` on the hub. That is the dashboard.

## Install a worker (Mac or Linux)

On each worker, as its owner:

```sh
curl -fsSL https://raw.githubusercontent.com/linuxdynasty/fleet-board/main/packaging/install.sh |
  sh -s -- --program fleet-agent
```

Same deal: verified download into `~/.local/bin/fleet-agent`, plus the
background service (a LaunchAgent on macOS, a user service on Linux).
On macOS, clear the quarantine flag as printed. On Linux, run once per
worker, with `sudo`, so the agent survives logout:

```sh
sudo loginctl enable-linger <user>
```

`fleet-agent doctor` checks this and tells you how to fix it. The Linux
service details (logs, restart, stop, PATH fixes) are in
[linux-agent.md](linux-agent.md).

## Join a worker to the hub

On the hub, make a one-time token for the machine:

```sh
fleetd enroll-token --machine NAME
```

Only the token goes to standard output (so `TOKEN=$(...)` works); the
expiry and the enroll line go to standard error. Tokens expire after
15 minutes by default (`--ttl`, at most 24 hours). The hub must be running
or the command says `fleetd is not running` and writes nothing.

On the worker, with the token:

```sh
fleet-agent enroll --hub <hub-address> --token "$TOKEN"
```

Port 7443 is used when none is given. Without `--hub` the agent looks for
the hub on the local network; pass `--hub` when a firewall hides it. Then
start the agent (or restart its service) and check `fleet-agent doctor`.

## Use the dashboard and fleet from another machine

The dashboard and `fleet` run on the hub only. From another machine,
forward the port over SSH, keeping the same port number on both sides:

```sh
ssh -L 7080:127.0.0.1:7080 <hub>
```

Then open `http://localhost:7080` there, or point `fleet` at it:

```sh
FLEET_URL=http://localhost:7080 fleet status
```

A different local port (say `-L 8080:...`) does not work: the hub only
accepts its own port's host names and refuses the rest.

## Staying current

Both programs update themselves the same way:

```sh
fleetd upgrade              # on the hub
fleet-agent upgrade         # on each worker
```

Each fetches the newest release (or `--version vX.Y.Z` for a named one),
checks the checksum, swaps its own binary, and restarts its service. A
bad checksum or an older release installs nothing; a failed restart is
reported after the swap. The databases update forward only when the new
program starts; going back to an older release is refused, because the
newer database would no longer open.

Upgrade one machine at a time, hub first. The hub and agents speak
versions that stay compatible (additive-only since `v0.1`).

To build a release yourself (owner tags only):

```sh
make gen                    # once per checkout
(cd web && npm run build)   # so fleetd embeds the dashboard
make dist VERSION=vX.Y.Z    # archives plus SHA256SUMS under dist/
```

Pushing the tag runs the same steps in CI and publishes to GitHub
Releases. Nothing is signed for Apple: the quarantine command above is
the whole story, once per machine.

## Where things live

| Machine | Program | Settings | State |
|---|---|---|---|
| Hub | `~/.local/bin/fleetd` | `~/.config/fleetd/fleetd.yaml` | `~/.local/state/fleetd/` (`state.db`, `logs.db`, `backups/`) |
| Worker | `~/.local/bin/fleet-agent` | `~/.config/fleet-agent/policy.yaml` | `~/.local/state/fleet-agent/` (`agent.db`) |

Service units: `~/Library/LaunchAgents/com.fleet-board.*.plist` on macOS,
`~/.config/systemd/user/fleetd.service` and `fleet-agent.service` on Linux.
Logs: `~/Library/Logs/fleetd.log` and `fleet-agent.log` on macOS,
`journalctl --user -u fleetd` / `fleet-agent` on Linux.

## When something is wrong

- `fleet-agent doctor` runs every local check with the fix next to it.
- A tampered or half-downloaded archive fails the checksum and installs
  nothing; download it again.
- `fleetd` refuses to start with no store (`fleetd init` first) or beside
  a running hub (one hub per state folder).
- An agent that exits before enrollment is normal: enroll, then restart
  its service (`systemctl --user reset-failed fleet-agent` first on Linux
  if it hit the restart limit).
- Clean-VM, four-machine, logout-survival and sleep checks need real
  machines and are owner-run: install on a fresh VM, enroll and upgrade
  there, log out of a Linux worker and confirm it still shows `ONLINE`,
  and sleep the hub to confirm it stays awake on power.
