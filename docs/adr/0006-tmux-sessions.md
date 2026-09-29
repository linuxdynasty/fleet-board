# 0006. tmux: fleet-owned servers and user-created sessions

- Status: Accepted
- Date: 2026-09-29
- Deciders: linuxdynasty

## Context

Every goal runs in a tmux window on a worker. The owner also wants to create
tmux sessions on any machine from the hub (ad-hoc interactive work, not only
goals), attach to them, and see what they are doing. The owner already uses
tmux by hand on these machines. The collector must detect activity, stuck
panes and on-screen prompts without polling everything constantly.

## Decision

**Servers.** The agent uses dedicated tmux servers (sockets) so fleet
activity never collides with the owner's own tmux:

| Server | Holds |
|---|---|
| `tmux -L fleet` | goal windows and sessions created from the hub |
| `tmux -L fleet-probe` | one hidden idle session per harness account for usage probes |
| default server | the owner's own sessions; untouched unless that machine's local policy allows `observe` or `control` |

**Creating or reusing sessions from the hub.** An `EnsureSession` command names
the machine, a session name and a working directory. It creates the session, or
reuses it if it already exists, and optionally starts a harness in it: a login
shell or a known harness (`claude`, `codex`, `muse`, …). Arbitrary commands are refused
unless the machine's local policy allows them
([0004](0004-agent-connectivity.md)). The request may target the `fleet` server
(default) or the owner's default server, so the session also shows up in their
usual `tmux ls`. Creating on the default server needs that machine's local
policy set to `default_server: control` ([0004](0004-agent-connectivity.md)). The agent reports the created session back with its target
(`session:window.pane`).

**Seeing the owner's existing sessions** on the default server is opt-in per
machine and read-only unless the owner turns on control for that machine.

**Observation.** The agent keeps one tmux control-mode connection (`tmux -C`)
per fleet server. tmux pushes output and window events, which drive live pane
streaming, stuck detection and "is a prompt showing". `capture-pane` is used
for snapshots and fingerprints. No per-pane polling loop.

**Attach.**
- v1: the UI shows a copyable `ssh -t <user>@<host> tmux -L fleet attach -t
  <target>` for native terminals.
- Later: browser attach. The agent streams pane output up its gRPC stream to an
  xterm.js view; keystrokes come back as typed input commands, with authz and
  audit per keystroke batch.

**Paths and trust.** The agent resolves absolute harness paths once with a
login shell at install time and again when a harness version changes, and
pre-trusts each repo checkout so a goal never stalls on the harness's
folder-trust prompt.

**Lifecycle.** Goal windows are closed a grace period after the goal is
verified merged. Sessions the owner created are never closed automatically.

## Consequences

- The owner's own tmux setup is untouched unless they opt in.
- Control mode gives push-style updates, at the cost of parsing its protocol.
  **Minimum tmux version: 3.2** on every worker; tested in CI against 3.2 and
  the latest release.
- Two extra tmux servers per worker (cheap).

**Session logs.** Every fleet session's terminal output is recorded for later
reading; see [0010](0010-session-logs.md).

## Open questions

- Whether browser attach is in v2 or v3.
- Default grace period before closing a merged goal's window.
