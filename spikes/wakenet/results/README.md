# Spike 0.6 results: wake and network-change detection (`wakenet`)

Issue #51, plan item 0.6, spec 01 A6. Question: how can `fleet-agent` notice,
without cgo, that its machine just woke from sleep or changed network, so it
reconnects at once instead of waiting for a dead connection to time out?

**Status:** cost measured on the Mac mini; routing-socket delivery measured with
a no-root trigger. **No real sleep or real network change has been observed
yet.** Those need the owner (sudo, or putting a machine to sleep): see
[OWNER RUN NEEDED](#owner-run-needed).

## The four methods

| Name | What it does | macOS source | Linux source |
|---|---|---|---|
| `clock` (a) | Every interval, compare how far the wall clock moved with how far Go's monotonic clock moved. Go's monotonic clock stops while the machine sleeps, the wall clock does not, so a gap over the threshold (default 2 s) means the machine slept. A negative gap means the wall clock was stepped back (logged as `wall_step_back`). | `time.Now()`, `Round(0)` | same |
| `sleepmark` (s) | Every interval, read the kernel's own record of sleep and report when it changes. Immune to wall-clock steps (NTP, manual changes). | `sysctl kern.waketime` (exact wake time; zero until the first wake since boot) | `CLOCK_BOOTTIME − CLOCK_MONOTONIC` (total time suspended; no wake timestamp) |
| `ifpoll` (b) | Every interval, list interfaces (up/running) and their addresses with `net.Interfaces()`/`Addrs()` and diff against the last list; also compare the IPv4 default route. | default route only with `-route-exec` (an exec of `route -n get default`) | `/proc/net/route` (file read, always on) |
| `push` (c) | Block on a kernel socket that pushes routing events; log link up/down, address add/remove and default-route changes as relevant, everything else only counted. | `PF_ROUTE` routing socket (`RTM_IFINFO`, `RTM_NEWADDR`, `RTM_DELADDR`, default `RTM_ADD/DELETE/CHANGE`) | `NETLINK_ROUTE` bound to `RTMGRP_LINK`, `IPV4/IPV6_IFADDR`, `IPV4/IPV6_ROUTE` |

Every log line is JSON: `time` (wall), `event`, `method`, `relevant`, plus
method fields. Wake events carry `latency_ms` (detection time minus
`kern.waketime`) on macOS. Relevant events carry `since_wake_ms` when the kernel
recorded a wake in the last ten minutes (macOS only). With `-trigger`, the first
relevant event per method after each trigger logs a `detected` line with
`latency_from_start_ms` (and `latency_from_end_ms` if it came after the command
returned). The run ends with a `summary` line: event counts and `getrusage` CPU
time and context switches for the process and its children.

Pure logic (jump detector, sleep-record detector, interface diff, both route
parsers, both push-message parsers) has table tests; per-OS tests check the
hard-coded kernel constants against `x/sys/unix`.

## Libraries

- `golang.org/x/sys` v0.48.0 (`unix.SysctlTimeval`, `unix.ClockGettime`,
  routing and netlink sockets). **Not listed in ADR 0020 or 0023; needs an ADR
  line before product use.** It is already in the product's module graph:
  `google.golang.org/grpc` v1.84.0 requires `golang.org/x/sys` v0.47.0.
- Nothing else. No cgo: every target builds with `CGO_ENABLED=0`.
- Considered, not built: Linux logind `PrepareForSleep` over D-Bus would give an
  exact suspend/resume push but needs `github.com/godbus/dbus/v5` (another ADR
  line). macOS IOKit power notifications (`IORegisterForSystemPower`) need cgo
  or `purego`, so they are out.

## Setup

- Mac mini, Apple M4 Pro, 12 cores, macOS 27.0.1 (26A434), 192.168.1.124, wired
  `en0`, 32 interfaces (many `utun`, `bridge`, `vmenet`). Not slept since boot
  (`kern.waketime` = 0). Go 1.27.1. Base commit 843df01.
- Shared host: other agents were active; load average 2.0 to 3.3 during runs.
  CPU numbers are per process (`getrusage`), so they are less sensitive to load
  than wall times.
- `top -stats idlew` reported 0 idle wakeups for every process, and
  `powermetrics` needs sudo, so wakeups are approximated by context switches.
  macOS reports almost every switch of a Go process as involuntary; voluntary is
  1 to 4 and carries no signal.

## Commands run (all rc=0)

```bash
cd spikes/wakenet
go vet ./...; echo rc=$?                                  # rc=0
GOOS=linux go vet ./...; echo rc=$?                       # rc=0
CGO_ENABLED=0 go test -count=1 ./...; echo rc=$?          # rc=0
for t in darwin/arm64 darwin/amd64 linux/amd64 linux/arm64; do
  CGO_ENABLED=0 GOOS=${t%/*} GOARCH=${t#*/} go build -o /tmp/wakenet-${t%/*}-${t#*/} .; echo "$t rc=$?"
done                                                      # all rc=0
go build -o /tmp/wakenet .
# cost: each config as its own process, 180 s, all configs in parallel, twice
/usr/bin/time -l /tmp/wakenet -duration 180s -methods <m> [-route-exec] [-interval 1s]
# delivery: 30 no-root triggers, each a routing lookup echoed as RTM_GET
/tmp/wakenet -methods push -push-relevant RTM_GET \
  -trigger '/sbin/route -n get 192.0.2.1 >/dev/null' -trigger-after 500ms -repeat 30 -duration 20s
```

Raw output: [mac-mini-cost-round1.txt](mac-mini-cost-round1.txt),
[mac-mini-cost-round2.txt](mac-mini-cost-round2.txt),
[mac-mini-push-delivery.txt](mac-mini-push-delivery.txt).

## Numbers (Mac mini)

### Cost, 180 s per run, interval 5 s unless noted

CPU is user + system of the `wakenet` process; "children" is the `route` execs.

| Config | Round 1 CPU | Round 2 CPU | CPU per second | Context switches (r1 / r2) | Max RSS |
|---|---|---|---|---|---|
| `none` (Go runtime idle baseline) | 3.0 ms | 2.9 ms | 0.016 ms/s | 85 / 90 | 5.5 MiB |
| `clock` | 6.0 ms | 6.2 ms | 0.034 ms/s | 227 / 262 | 5.7 MiB |
| `sleepmark` | 7.8 ms | 7.4 ms | 0.042 ms/s | 273 / 261 | 5.9 MiB |
| `ifpoll` | 44.0 ms | 43.0 ms | 0.24 ms/s | 858 / 992 | 8.7 MiB |
| `ifpoll -route-exec` | 86.3 ms + 89.3 ms children | not rerun | 0.97 ms/s total | 2681 | 9.2 MiB |
| `push` | 13.1 ms | 11.5 ms | 0.068 ms/s | 597 / 443 | 5.8 MiB |
| all four | 57.4 ms | 53.4 ms | 0.31 ms/s | 1609 / 1511 | 9.2 MiB |
| all four, interval 1 s | 259.3 ms | 190.7 ms | 1.2 ms/s | 6242 / 5401 | 12 MiB |

Per unit of work, from the table minus the baseline (36 ticks per run): one
`clock` tick about 0.09 ms CPU, one `sleepmark` tick about 0.13 ms, one `ifpoll`
tick about 1.1 ms (32 interfaces), one `route -n get default` exec about 2.4 ms
in the child plus about 1.2 ms in the parent, one routing-socket message about
0.15 ms. Every config is far below 1 % of one core; `ifpoll` is about 80 % of
the "all four" cost and the `route` exec doubles it.

Round 1's `push` saw 113 messages, but about 36 were `RTM_GET` echoes of the
parallel `-route-exec` run's lookups. Round 2 (no `-route-exec`, every message
logged) saw 59, all `RTM_MISS` (other processes' route lookups that found no
route), about one every 3 s; none relevant.

### False positives

Across both rounds (`clock` and `sleepmark` in 3 processes, `ifpoll` and `push`
in 3 processes each, 180 s each), there were zero `wake_suspected`, zero `wake`,
zero `net_change` and zero relevant push events. Nothing on the machine slept or
changed network, so this is the expected answer; it is a short sample.

### Routing-socket delivery (no-root proxy)

30 triggers, each `/sbin/route -n get 192.0.2.1` (a lookup only, no traffic).
The kernel echoes the request as `RTM_GET` to every routing socket.

| | p50 | p90 | max | min |
|---|---|---|---|---|
| trigger start to `RTM_GET` received | 27.7 ms | 30.0 ms | 30.4 ms | 4.8 ms |
| trigger command runtime (`sh` + `route`) | 28.6 ms | | 31.3 ms | 5.1 ms |

All 30 arrived; each arrived before the `route` command had returned (about
0.9 ms before at p50), so nearly all of the 28 ms is starting `sh` and `route`
and the kernel-to-listener delivery is under a millisecond. This proves delivery
speed for a message the kernel does send; it does not prove which messages macOS
sends on a real link or address change or on wake.

## Not tested here, and why

| Item | Why not |
|---|---|
| Real sleep and wake (macOS) | Putting this machine to sleep is forbidden; the Mac mini has not slept since boot. Needs the MacBook run below. |
| Suspend and resume (Linux) | No access to other machines; needs the omarchy run below. |
| Network-change latency of `ifpoll` and `push` with a real interface or address change | Every trigger that changes an interface or address needs root: `ifconfig lo0 alias` and `route add` need sudo, and creating a `utun` as a normal user fails with "operation not permitted" (tested with a throwaway program; nothing was created). A ping to unassigned `127.0.0.2` produced no routing message. Turning Wi-Fi or Ethernet off is forbidden. |
| Linux numbers of any kind | The Linux binary builds and `GOOS=linux go vet` passes; the netlink parser has table tests; nothing ran on Linux. |
| Wake latency of `clock` and `sleepmark` | Theory: Go timers run on the monotonic clock, which stops during sleep, so the first tick after wake fires within one interval (≤ 5 s). Not observed. |
| macOS dark wake (Power Nap) | Unknown whether `kern.waketime` moves on a dark wake. The MacBook run shows it if `pmset -g log` lists a `DarkWake`. |

## OWNER RUN NEEDED

The branch `spike/0.6-wake-netchange` is local until the coordinator pushes it.
Build on the Mac mini from the worktree and copy the binaries (`scp` adds no
quarantine flag; if a binary arrives another way, run
`xattr -d com.apple.quarantine /tmp/wakenet` once):

```bash
cd /Volumes/Development/personal-repos/fleet-board-worktrees/prep-0.6-wake/spikes/wakenet
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -o /tmp/wakenet-darwin-arm64 .
CGO_ENABLED=0 GOOS=linux  GOARCH=amd64 go build -o /tmp/wakenet-linux-amd64 .
scp /tmp/wakenet-darwin-arm64 192.168.1.118:/tmp/wakenet
scp /tmp/wakenet-linux-amd64  192.168.1.150:/tmp/wakenet
```

(Use `GOARCH=amd64` for an Intel Mac and `GOARCH=arm64` for an ARM Linux box.)

### Step 0 (optional, safe on any Mac, including this Mac mini): network-change latency

Adds and removes a loopback alias five times; it does not touch real
connectivity. Needs sudo once (`sudo -v` caches it so `-n` does not prompt).

```bash
sudo -v
/tmp/wakenet -methods ifpoll,push -interval 5s \
  -trigger 'sudo -n /sbin/ifconfig lo0 alias 10.254.254.1 netmask 255.255.255.255' \
  -untrigger 'sudo -n /sbin/ifconfig lo0 -alias 10.254.254.1' \
  -repeat 5 -duration 2m | tee /tmp/wakenet-mac-net.jsonl
ifconfig lo0 | grep 10.254.254.1   # must print nothing (alias removed)
```

### Step 1: real sleep and wake on the hub MacBook (192.168.1.118)

If `fleetd` already runs there under `caffeinate -s`, the Mac will not sleep on
AC power: stop it or unplug first (`pgrep -fl caffeinate` shows it).

```bash
/tmp/wakenet -interval 5s -route-exec | tee /tmp/wakenet-mac-wake.jsonl
```

1. Leave it running. Apple menu > Sleep (or close the lid).
2. Wait at least 2 minutes. Wake the Mac and unlock it.
3. Wait 30 seconds, then press Ctrl-C (the `summary` line prints).
4. Optional network change: run the command again, turn Wi-Fi off and on from
   the menu bar (this drops the laptop's own network for a few seconds), wait
   30 s, Ctrl-C, saving to `/tmp/wakenet-mac-wifi.jsonl` instead.
5. Then run:

```bash
pmset -g log | grep -E " (Sleep|Wake|DarkWake) " | tail -8
sysctl kern.waketime kern.sleeptime
```

### Step 2: suspend/resume and a network change on Linux (omarchy, 192.168.1.150)

Suspending omarchy pauses any running Codex goal and drops its connections;
pick a time with no goal running. Run inside tmux so an SSH drop does not kill
the probe.

```bash
tmux new -s wakenet
cat /sys/power/mem_sleep                 # note which mode is in [brackets]
/tmp/wakenet -interval 5s | tee /tmp/wakenet-linux-wake.jsonl
```

In a second tmux window (Ctrl-b c):

```bash
sudo rtcwake -m mem -s 90                # suspend now; the RTC alarm wakes it after 90 s
# if rtcwake is missing: systemctl suspend, then wake with a key press
```

After it wakes, wait 30 s, press Ctrl-C in the first window, then:

```bash
journalctl -k -b --since "-15 min" -o short-iso-precise | grep -E "PM: suspend (entry|exit)"
```

Network change on Linux (loopback address, does not touch real connectivity):

```bash
sudo -v
/tmp/wakenet -methods ifpoll,push -interval 5s \
  -trigger 'sudo -n ip addr add 10.254.254.1/32 dev lo' \
  -untrigger 'sudo -n ip addr del 10.254.254.1/32 dev lo' \
  -repeat 5 -duration 2m | tee /tmp/wakenet-linux-net.jsonl
ip addr show dev lo | grep 10.254.254.1  # must print nothing (address removed)
```

### What to send back

The whole `/tmp/wakenet-*.jsonl` files (they hold timestamps, interface names
and local addresses only; by default push logs only relevant messages), plus the
`pmset`, `sysctl`, `/sys/power/mem_sleep` and `journalctl` output above. The
lines that decide the result:

- `"event":"wake_suspected"` (clock) and `"event":"wake"` (sleepmark), with
  `latency_ms` on macOS; on Linux compare their `time` with the journal's
  `PM: suspend exit` line.
- `"event":"net_change"` / `"default_route_change"` (ifpoll) and relevant
  `RTM_*` lines (push), with `since_wake_ms` on macOS: does the push socket see
  the wake as a link or address change, and how soon?
- `"event":"detected"` lines: per-method latency from each trigger.
- `"event":"summary"`: CPU cost on that machine.

## Recommendation (proposed, not yet observed on a real wake)

- **macOS:** wake from `sleepmark` (`kern.waketime` read every 5 s through
  `x/sys/unix`): exact wake time, immune to clock steps, about 0.04 ms CPU per
  second. Keep `clock` as the zero-dependency fallback (about 0.03 ms/s).
  Network change from the `PF_ROUTE` push socket, filtered to link, address and
  default-route messages and debounced (bursts are expected), plus ignoring
  noisy interfaces (`awdl`, `llw`, `utun`, `bridge`) if the owner runs show
  them flapping. No `ifpoll` loop and no `route` exec: polling costs about 3.5×
  the push socket here and the exec doubles that.
- **Linux:** wake from `sleepmark` (`CLOCK_BOOTTIME − CLOCK_MONOTONIC` every
  5 s), `clock` as fallback. Network change from `NETLINK_ROUTE` with the link,
  address and route groups (no root needed). If 5 s after resume is too slow,
  logind `PrepareForSleep` over D-Bus is the exact push source, at the cost of
  `godbus/dbus` and an ADR line.
- Both: one new direct dependency, `golang.org/x/sys`, which needs an ADR line.
  Whatever the owner runs show, any wake or relevant network event should only
  trigger "reconnect now"; a missed event still falls back to the normal
  heartbeat timeout, so a false positive costs one reconnect.

## What this does not prove

Wake detection on any machine; which push messages macOS and Linux send on a
real link or address change, or on wake; dark-wake behavior; Linux cost; cost on
a laptop with Wi-Fi roaming, where `awdl0` and other interfaces may flap.
