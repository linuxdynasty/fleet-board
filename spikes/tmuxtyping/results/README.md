# Spike 0.5: can tmux tell the agent when a person types?

Plan item 0.5, issue #50. Three questions:

1. Does `client_activity` (the time a client last sent a key, meaning a person
   typing in an attached pane) reach a tmux control-mode client as a push (a
   message tmux sends on its own), either as a `%` notification or through a
   `refresh-client -B` subscription (tmux re-checks a format about once a
   second and sends `%subscription-changed` when its value changes)? If yes,
   typing detection needs no 2 s `list-clients` poll (02h E13).
2. Which tmux release added the control-mode flags `pause-after` and
   `ignore-size` (01 E2)?
3. How do we install tmux 3.2 on a GitHub Actions Ubuntu runner (plan row 7)?

## Answers

**1. Yes, there is a push source, from tmux 3.2 on, but not the one the plan
named.** tmux sends no `%` notification when a person types, and
`#{client_activity}` inside a subscription is the control client's own time
(it never moved in 240 trials). What works is a subscription on
`#{session_activity}`: tmux moves it to the current second when an attached
client presses a key. One subscription covers every session through the
`#{S:...}` loop. The `%subscription-changed` line came 2 to 1,002 ms after the
keystroke in 120 of 120 typing trials on the Mac (60 per version; median 415 to
553 ms) and 30 of 30 in Linux. The agent's own `send-keys` never moved it
(0 of 60), and nothing moved while idle. So typing detection can drop the 2 s
`list-clients` poll: a change says which session; one `list-clients` read at
that moment says which client and pane. On tmux 3.4 and later the `#{L:...}`
client loop also gives each client's `client_activity` in one subscription.
An `arbiter-fleet` ruling made that change; see "Decision" below.

**2. tmux 3.2 added both flags.** tmux's CHANGES file lists both under
"CHANGES FROM 3.1c TO 3.2" (quoted below). Run on each release: 3.1c refuses
`attach-session -f` ("unknown option -- f") and `refresh-client -f` (usage
error). 3.2a and 3.7c accept both forms, list them in `client_flags`,
`ignore-size` stops the control client from shrinking the window, and
`pause-after` makes tmux send `%extended-output` and, sometimes, `%pause` (see
finding 7).

**3. On `ubuntu-22.04`, `sudo apt-get install -y tmux` gives tmux 3.2a**
(package `3.2a-4ubuntu0.2`: upstream 3.2a plus 5 small Ubuntu patches).
`ubuntu-24.04` (`ubuntu-latest`) gives 3.4 instead, and neither runner image
ships tmux already. For an exact upstream 3.2a, or for the latest release, build
from the official tarball with [`../install-tmux.sh`](../install-tmux.sh)
(checks the SHA-256 first). Both ways were proved in an `ubuntu:22.04`
linux/amd64 container on this Mac, and the from-source 3.2a build on macOS too.

## Setup

- Machine: Mac16,11 (Apple M4 Pro), macOS 27.0.1 (26A434), Go 1.27.1,
  `CGO_ENABLED=0`, creack/pty v1.1.24 (pure Go). Branch
  `spike/0.5-tmux-typing` from `6c6afb8`, 2026-10-08.
- Linux: Docker (OrbStack) `ubuntu:22.04` image
  `sha256:5ec03bb3441e8b0bf3b4f9cd4629a1ae763010dc3035bb8da3ae6cf026486401`,
  `--platform linux/amd64` (x86_64 emulated on Apple silicon), Ubuntu 22.04.5.
- tmux builds:

| tmux | From | SHA-256 of the tarball | Checked against |
|---|---|---|---|
| 3.1c | GitHub release tarball, built into `/tmp` | `918f7220447bef33a1902d4faff05317afd9db4ae1c9971bef5c787ac6c88386` | not published by GitHub for this release; used only to show the flags were missing |
| 3.2a | GitHub release tarball, built into `/tmp` | `551553a4f82beaa8dadc9256800bcc284d7c000081e47aa6ecbb6ff36eacd05f` | GitHub publishes no digest for this old asset; Ubuntu's `tmux_3.2a-4ubuntu0.2.dsc` lists the same hash, and Ubuntu's `tmux_3.2a.orig.tar.gz` is byte-identical (`cmp` rc=0) |
| 3.7c | Homebrew `/opt/homebrew/bin/tmux` (Mac); tarball build (Linux) | `7c60cae9a0e25288e2e24750aafc9e8800fc7fd4555e447e1b29ee4201cfb3bf` | GitHub release API `digest` field, equal |
| 3.2a (Ubuntu) | `apt-get install tmux` in the container | package `3.2a-4ubuntu0.2` | Launchpad: jammy-updates and jammy-security publish this version |

- macOS build: `CPPFLAGS=-I…/libevent/include -I…/ncurses/include`,
  `LDFLAGS=-L…/libevent/lib -L…/ncurses/lib` (Homebrew libevent 2.1 and
  ncurses), `./configure --prefix=/tmp/fleetprobe-05/prefix-3.2a && make -j8
  && make install`: configure, make and install rc=0 for 3.2a and 3.1c;
  `tmux -V` prints `tmux 3.2a`.
- The probe ([`..`](..)) starts a throwaway server with `-L fleetprobe-<name>
  -f /dev/null` (it refuses any other socket name) and a clean environment
  without `TMUX`. Each session runs `exec cat`. A control client
  (`tmux -C attach`) holds the subscriptions. A person is simulated by a normal
  `tmux attach` client on a pseudo-terminal the probe owns, 100x30. Before
  every key the probe reads the pane (`pane_current_command` must be `cat`,
  then `capture-pane`) and stops if it is anything else.
- Typing trials, 4 kinds in turn, each after a random 0 to 1 s wait (so the
  key lands at a random point of tmux's 1 s timer), then 2.5 s of watching:
  `type-work` (a key in the session the control client is attached to),
  `type-other` (a key in another session), `agent-sendkeys` (the control
  client's own `send-keys -l b`), `idle` (nothing). After each trial the probe
  reads `list-clients` (what the 2 s poll would see).
- The Mac typing runs used `cat` instead of `exec cat`; macOS `sh` runs cat in
  place, and the probe's own check saw `cat` before every key. Ubuntu's `dash`
  does not, so the probe switched to `exec cat` before the Linux runs.

## Commands and exit codes

```bash
cd spikes/tmuxtyping && CGO_ENABLED=0 go build -o /tmp/fleetprobe-05/tmuxtyping .   # rc=0
./tmuxtyping typing -tmux /tmp/fleetprobe-05/prefix-3.2a/bin/tmux -L fleetprobe-typing-3.2a -n 30 > typing-3.2a.jsonl   # rc=0
./tmuxtyping typing -tmux /opt/homebrew/bin/tmux -L fleetprobe-typing-3.7c -n 30 > typing-3.7c.jsonl                    # rc=0
./tmuxtyping flags -tmux /tmp/fleetprobe-05/prefix-3.1c/bin/tmux -L fleetprobe-flags-3.1c > flags-3.1c.jsonl            # rc=0
./tmuxtyping flags -tmux <3.2a or 3.7c> -L fleetprobe-flags-<v>-<i> >> flags-<v>.jsonl   # 5 runs each, all rc=0
./tmuxtyping summarize typing-3.2a.jsonl typing-3.7c.jsonl > summary-mac.md             # rc=0
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o /tmp/fleetprobe-05/linux/tmuxtyping .  # rc=0
docker run --rm --platform linux/amd64 -v /tmp/fleetprobe-05/linux:/work ubuntu:22.04 bash /work/run.sh   # rc=0
```

`run.sh` was a throwaway script in `/tmp/fleetprobe-05/linux` and was not
kept. Its steps are listed next, its output is
[linux-container-run.txt](linux-container-run.txt), and the raw logs are
`flags-linux-*.jsonl` and `typing-linux-*.jsonl`. Inside the container:
the apt install (rc=0), `install-tmux.sh` for 3.2a and 3.7c (rc=0, `sha256sum
-c` OK), `install-tmux.sh` with a wrong hash (rc=1, "checksum did NOT match",
nothing built), then `flags -stall 10s` and `typing -n 5` for apt 3.2a,
source 3.2a and source 3.7c (all rc=0).

## Typing numbers

Mac, 30 trials per kind per version ([summary-mac.md](summary-mac.md), raw in
`typing-3.2a.jsonl` and `typing-3.7c.jsonl`). "ms" is from the keystroke to the
moment the probe read the line.

| tmux | Trial | Signal | Arrived | Min ms | Median ms | Max ms |
|---|---|---|---|---|---|---|
| 3.2a | type-work | `sess_act` = `#{session_activity}` | 30/30 | 8 | 415 | 926 |
| 3.2a | type-other | `all_sess_act` = `#{S:#{session_name}=#{session_activity} }` | 30/30 | 2 | 553 | 999 |
| 3.2a | agent-sendkeys | `sess_act` or `all_sess_act` | 0/30 | | | |
| 3.2a | idle | any subscription | 0/30 | | | |
| 3.7c | type-work | `sess_act` | 30/30 | 32 | 487 | 1002 |
| 3.7c | type-work | `clients_act` = `#{L:#{client_pid}=#{client_activity} }` | 30/30 | 32 | 487 | 1002 |
| 3.7c | type-other | `all_sess_act` and `clients_act` | 30/30 | 46 | 482 | 992 |
| 3.7c | agent-sendkeys | `sess_act`, `all_sess_act`, `clients_act` | 0/30 | | | |
| 3.7c | idle | any subscription | 0/30 | | | |
| both | type-work, agent-sendkeys | `%output` of the pane (cat echoes the key) | 30/30 | 0 | 0 to 1 | 2 |
| both | type-* | `list-clients` read after the trial shows the new `client_activity` | 30/30 | | | |

In every typing trial the pushed value equals the second of the keystroke.
`ctl_client_act` (`#{client_activity}` in a subscription) never changed after
its first value. `win_act` (`#{window_activity}` on `@*`) moved for typing and
for the agent's `send-keys` alike, because it follows pane output.

Linux container, 5 trials per kind per build ([summary-linux.md](summary-linux.md)):
`sess_act` arrived 5/5 for type-work (apt 3.2a 200 / 345 / 850 ms min / median /
max; source 3.2a 234 / 389 / 821; source 3.7c 291 / 428 / 1009),
`all_sess_act` 5/5 for type-other, 0/5 for agent-sendkeys and idle.

## Findings

1. **No `%` notification for input.** In the 2.5 s after a keystroke the only
   lines were the pane's `%output` (cat echoing the key) and, once per window
   on its first output, `%window-renamed @0 cat`. tmux's `control-notify.c`
   has no notification for client input in 3.2a or 3.7c.
2. **`#{client_activity}` in a subscription is the control client's own.**
   `control.c` builds the subscription's format with the control client
   (`format_create_defaults(NULL, c, s, …)`), and the control client sends
   commands, not keys.
3. **`#{session_activity}` moves on key input** (`server_client_key_callback`
   calls `session_update_activity`), and also on attach, `new-session`,
   `switch-client`, pane focus-in and a client waking from suspend
   (`session_update_activity` call sites in 3.2a). So a change means "a client
   acted in this session"; mouse use counts as input too, as it already does
   for `client_activity`.
4. **The agent's own keys do not count.** `send-keys` goes straight to the
   pane, not through a client, so `session_activity` and `client_activity`
   stay put (0 of 60 on the Mac, 0 of 15 in Linux).
5. **Granularity is one second.** tmux re-checks subscriptions on a 1 s timer
   and the value is whole seconds, so a person typing steadily produces at most
   one line per session per second, and a change arrives within about 1 s.
   The current poll is up to 2 s.
6. **`#{L:...}` needs tmux 3.4.** On 3.2a the subscription is accepted but
   prints the literal `L:` text with one value and never changes (CHANGES
   lists "Add an L modifier like P, W, S to loop over clients" under 3.3a to
   3.4). The `client-active` hook (3.3) was not tested; the source fires it only
   when the latest client of a window changes, not on every key.
7. **`pause-after` is set on 3.2, but a stalled reader does not always get a
   `%pause`.** The probe stopped reading the control client for 3 s while a
   pane ran `yes`. `%pause` came in 5 of 5 runs on 3.2a and 2 of 5 on 3.7c
   (Mac, idle machine, the committed logs), and 0 of 3 in the Linux container
   even with a 10 s stall. Earlier single runs, whose logs were overwritten,
   gave 3.2a 1 of 2 (the miss while the container build was running) and
   3.7c 3 of 3. `control.c` explains the spread: once 512 bytes are waiting for
   a control client, tmux stops taking new output for it, and only output
   already queued can age past `pause-after`. When it came, `%pause` arrived 0
   to 12 ms after reading resumed (it was written during the stall), and
   `refresh-client -A '%N:continue'` answered with `%continue %N` inside its
   own `%begin`/`%end` reply, not as a separate line.
8. **3.1c's `-F` hides the problem.** `refresh-client -F ignore-size` and
   `-F pause-after=1` return success on 3.1c and, read from its source (not
   measured: 3.1c prints no `client_flags`), do nothing (its code says
   "Unknown flags are ignored"). A version check must use `-f`, which fails on
   3.1c, or read `client_flags`.
9. **Set `ignore-size` at attach.** With `window-size smallest`, a control
   client at 60x20 shrank a 100x30 person's window to 60x20 (all versions).
   Attached with `-f ignore-size`, the window stayed 100x29 (3.2a, 3.7c). Turned
   on later with `refresh-client -f`, the window stayed 60x20 until the next
   size change (`refresh-client -C` again), then went to 100x29.

CHANGES, "CHANGES FROM 3.1c TO 3.2" (identical in the 3.2a and 3.7c tarballs):

> Add support for pausing a pane when the output buffered for a control mode
> client gets too far behind. The pause-after flag with a time is set on the
> pane with refresh-client -f and a paused pane may be resumed with
> refresh-client -A.

> Change the existing client flags for control mode to apply for any client,
> use the same mechanism for the read-only flag and add an ignore-size flag.
> refresh-client -F has become -f (-F stays for backwards compatibility) and
> attach-session and switch-client now have -f flags also.

> Add a way for control mode clients to subscribe to a format and be notified
> of changes rather than having to poll.

## CI install (question 3)

```yaml
# tmux 3.2a from Ubuntu (3.2a plus Ubuntu patches); only on ubuntu-22.04
- run: sudo apt-get update && sudo apt-get install -y --no-install-recommends tmux && tmux -V
# exact upstream releases on any Ubuntu runner (3.7c's configure needs bison)
- run: |
    sudo apt-get update
    sudo apt-get install -y --no-install-recommends build-essential pkg-config bison libevent-dev libncurses-dev
    spikes/tmuxtyping/install-tmux.sh 3.2a 551553a4f82beaa8dadc9256800bcc284d7c000081e47aa6ecbb6ff36eacd05f "$HOME/tmux-3.2a"
    spikes/tmuxtyping/install-tmux.sh 3.7c 7c60cae9a0e25288e2e24750aafc9e8800fc7fd4555e447e1b29ee4201cfb3bf "$HOME/tmux-3.7c"
```

The source builds were not timed separately; caching `$HOME/tmux-*` keyed on
version and hash would skip them after the first run. The apt path
ends when GitHub retires the `ubuntu-22.04` image; the source path does not.

## Decision

An `arbiter-fleet` ruling (recorded in the spike PR, with PR 7's proofs on
#35) made the `#{session_activity}` subscription plus one `list-clients` read
per change the rule, and the 2 s `list-clients` poll only a backup while the
subscription is not confirmed. No new ADR: ADR 0022 A.6 prefers a push source,
and ADR 0024 decision 9 allows the poll without requiring it. 02h E13,
defaults.md and plan rows 0.5 and 7 apply it.

## Not proved

- Not on a real GitHub Actions runner: Linux ran in an emulated amd64 container
  on this Mac. No CI workflow was changed.
- Browser attach through a grouped view session (01 J1) was not tested.
  Typing there would move the view session's `session_activity`; the `S:` loop
  sees it, but the agent must map the view session back to the original.
- Real harness panes, many sessions, and the cost to tmux of expanding the
  `S:` loop every second were not measured. Mouse and focus activity were read
  from the source, not measured.
- The `%pause` trigger was not made reliable (finding 7); a CI test that
  expects `%pause` from a plain read stall would be flaky.
- tmux 3.2 (without the "a") and 3.3 to 3.7b were not run; the CHANGES file
  was used for them.

## Cleanup

Every server the probe started was `-L fleetprobe-*` and every run ends with
`kill-server` and `has-session` failing (`"server_gone":true` in each log). One
server left by a probe process killed by the tool (`fleetprobe-typing-3.2a`)
was killed by name. The leftover `fleetprobe-*` socket files were deleted, and
so was the then-empty `/private/tmp/tmux-501` folder, which did not exist
before the probe. No
other tmux server was started, attached, or sent a command. No coding
harness was started. No credential, auth file or config was read; tmux
ran with `-f /dev/null`. The builds and tarballs in `/tmp/fleetprobe-05`
were deleted after the run; the `ubuntu:22.04` image stays in the local
Docker cache, and the containers ran with `--rm`. Pane captures held only the
probe's own `a`/`b` keys; only the JSON logs above were kept.
