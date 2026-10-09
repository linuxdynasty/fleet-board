# Spike 0.6 results: wake and network-change detection (`wakenet`)

Issue #51, plan item 0.6, spec 01 A6. Question: how can `fleet-agent` notice,
without cgo, that its machine just woke from sleep or changed network, so it
reconnects at once instead of waiting for a dead connection to time out?

**Status: Done (plan item 0.6, ADR 0029).** Cost measured on the Mac mini;
routing-socket delivery measured with a no-root trigger; real network changes
seen on omarchy (Linux) and the MacBook (macOS) by the push socket and the
interface poll; Linux push noise measured. **No real sleep has been observed,
and none will be forced:** the owner does not put any machine to sleep. Wake
detection rests on the Go runtime and OS clock rules below, fake-clock tests in
PR 4, and a check on the first natural laptop sleep (#132).

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
  routing and netlink sockets). It is already a direct requirement of the
  product's `go.mod` (v0.48.0), used by the hub's peer-credential check
  (`internal/hub/api/peercred_*.go`) and terminal code
  (`internal/hub/backup/`), with no ADR line until
  [ADR 0029](../../../docs/adr/0029-wake-and-network-change-detection.md).
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

## Live observations (2026-10-08 to 2026-10-09)

Both probes ran `/tmp/wakenet -interval 5s -duration 48h` with all four methods;
the MacBook run adds `-route-exec`.

- omarchy: Linux, wired `enp3s0`, NVIDIA GTX 1080 Ti; started 2026-10-08
  02:15:25.
- MacBook: macOS 27.0.1, arm64; started 2026-10-08 14:15:50.

Both were still running when these numbers were taken (2026-10-09, about
11:10), so neither has printed its `summary` line yet.

**Real network changes.** The push socket saw each one at once; the 5 s poll saw
it 1 to 5 s later. Each burst of push messages lasted under 10 ms.

- omarchy, 05:10:30 (2026-10-08): a VPN interface `tun0` came up. Push:
  `RTM_NEWLINK` down then up, index 2387, at 05:10:30 (then two `RTM_NEWADDR` on
  index 2387 within 9 ms); ifpoll: `net_change` `iface_added` `tun0` at
  05:10:31.
- MacBook, 2026-10-08 17:03:24.084: push `RTM_DELADDR` and `RTM_IFINFO` down
  (index 36, `utun10`); ifpoll `iface_removed` `utun10` at 17:03:25.437
  (+1.35 s).
- MacBook, 2026-10-09 08:54:25.749/.754/.755: push `RTM_IFINFO` down, down, up
  and `RTM_NEWADDR` (index 36); ifpoll `iface_added` `utun10` at 08:54:30.661
  (+4.9 s).

**Linux push noise.** omarchy logged 972 `RTM_NEWADDR` "relevant" messages in
32.9 h, 970 on index 2 (`enp3s0`) and 2 on index 2387 (`tun0`). Gaps between
them: p10 4.6 s, p50 38.5 s, p90 114.5 s. ifpoll saw no change for them.
`enp3s0` has a dynamic IPv6 ULA (a private IPv6 address, `fdc7:…/64`,
`valid_lft` about 1690 s) refreshed by router advertisements, and each refresh
re-sends `RTM_NEWADDR`. So a push message alone must never mean "reconnect".

**A gap in the omarchy log.** There are no lines from 2026-10-08 13:33:09 to
2026-10-09 10:05:52 (20.6 h). The last line before the gap stops in the middle
of a record, and the next record follows on the same line (raw line 898, kept in
the extract). The same process wrote the same file the whole time (checked with
`ps` and `/proc/<pid>/fd/1` on 2026-10-09), so its writes most likely failed
during the gap. omarchy's `/tmp` is a 32 GB in-memory file system, 72 % full
when checked; a full `/tmp` is a likely cause but was not checked. So only about
12.4 of the 32.9 hours were logged, and the 972 messages and the gaps above come
from those hours (the 20.6 h hole is only the largest gap).

**One aborted suspend** on omarchy, 2026-10-08 15:56:02 to 15:56:03: the NVIDIA
driver refused ("nv_pmops_suspend returns -5") and "PM: suspend exit" came
about 1 s later. It falls inside the log gap, so the probe's log cannot show what
the probe saw. The kernel's own record of time asleep (`CLOCK_BOOTTIME` minus
`CLOCK_MONOTONIC`) read 0.0 s on 2026-10-09, so the kernel counted no sleep, and
a clock gap of about 1 s is under the 2 s threshold.

**No wake.** Zero `wake`, `wake_suspected` or `wall_step_back` events on either
machine (no sleep happened; on omarchy, over the logged hours). The earlier Mac
mini rounds also had zero false positives.

Extracts of the deciding lines (the raw logs are not committed):
[omarchy-live-extract.jsonl](omarchy-live-extract.jsonl) holds raw lines 1 to
7, 254 to 258, 897 to 899 and 980 of 980 (start, three sample refreshes, the
`tun0` burst, the lines around the gap, the last line; the gap line is not valid
JSON). [macbook-live-extract.jsonl](macbook-live-extract.jsonl) is the whole
12-line MacBook log, with its host name shortened to `MacBook`.

Counting commands:

```bash
python3 - wakenet-linux-wake.jsonl <<'EOF'
import collections, json, re, sys
from datetime import datetime
ts, idx = [], collections.Counter()
for line in open(sys.argv[1]):
    try:
        d = json.loads(line)
    except ValueError:
        continue  # the cut-off line at the gap
    if d.get("event") == "RTM_NEWADDR":
        ts.append(datetime.fromisoformat(re.sub(r"(\.\d{6})\d+", r"\1", d["time"])))
        idx[d["index"]] += 1
gaps = sorted((b - a).total_seconds() for a, b in zip(ts, ts[1:]))
n = len(gaps)
print(len(ts), dict(idx), round((ts[-1] - ts[0]).total_seconds() / 3600, 1))
print(round(gaps[n // 10], 1), round(gaps[n // 2], 1), round(gaps[9 * n // 10], 1), round(gaps[-1]))
EOF
# 972 {2: 970, 2387: 2} 32.9
# 4.6 38.5 114.5 74013
journalctl -k --since "2026-10-08 13:25" --until "2026-10-09 10:10" -o short-iso | rg -i "PM: |nv_pmops"
python3 -c "import time; print(round(time.clock_gettime(time.CLOCK_BOOTTIME) - time.clock_gettime(time.CLOCK_MONOTONIC), 3))"   # -0.0
```

## Why the wake method holds without a live sleep

Go 1.27.1 source, paths relative to `$(go env GOROOT)`:

- On macOS, Go's monotonic clock is `mach_absolute_time` on arm64 and amd64:
  `nanotime1` (`src/runtime/sys_darwin.go:286-304`) calls a trampoline that
  calls `libc_mach_absolute_time` (`src/runtime/sys_darwin_arm64.s:145-147`,
  `src/runtime/sys_darwin_amd64.s:90-92`). The wall clock is `CLOCK_REALTIME`
  (`src/runtime/sys_darwin_arm64.s:137-140`).
- Apple's SDK header `usr/include/mach/mach_time.h:58-62` describes
  `mach_continuous_time` as "like mach_absolute_time, but advances during
  sleep". So `mach_absolute_time` stops while the Mac sleeps.
- On Linux, Go's monotonic clock is `CLOCK_MONOTONIC`
  (`src/runtime/sys_linux_amd64.s:270`, `src/runtime/sys_linux_arm64.s:355`).
  The `clock_gettime(2)` man page (man-pages 6.19) says `CLOCK_MONOTONIC` "does
  not count time that the system is suspended", and `CLOCK_BOOTTIME` "also
  includes any time that the system is suspended".
- Go's own `time` package agrees (`src/time/time.go:54-59`): "On some systems
  the monotonic clock will stop if the computer goes to sleep."

So after a sleep of S seconds the wall clock has moved about S seconds more than
the monotonic clock, which is what `clock` measures (`jump.go:26-35`), and the
kernel's sleep record changes, which is what `sleepmark` reads. Both have table
tests with fake readings. If a wake is missed anyway, transport keepalive (ADR
0004) finds the dead connection within about 30 s, and the snapshot on connect
puts the state right; a false wake costs one reconnect.

## Not tested here, and why

| Item | Why not |
|---|---|
| Real sleep and wake (macOS and Linux) | No machine is put to sleep for this; the Mac mini has not slept since boot, and no laptop slept during the live runs. Checked on the first natural sleep (#132). |
| Network-change latency from a triggered change | Every trigger that changes an interface or address needs root: `ifconfig lo0 alias` and `route add` need sudo, and creating a `utun` as a normal user fails with "operation not permitted" (tested with a throwaway program; nothing was created). A ping to unassigned `127.0.0.2` produced no routing message. Turning Wi-Fi or Ethernet off is forbidden. Real changes were seen instead (live observations). |
| Linux CPU cost | The omarchy run had not printed its `summary` line when these numbers were taken. |
| Wake latency of `clock` and `sleepmark` | Theory: Go timers run on the monotonic clock, which stops during sleep, so the first tick after wake fires within one interval (≤ 5 s). Not observed. |
| macOS dark wake (Power Nap) | Unknown whether `kern.waketime` moves on a dark wake. |
| The MacBook's kernel sleep record | Not read: no remote access to the MacBook from the Mac mini. Its log shows no wake. |

## Opportunistic check

If a laptop running `wakenet` or `fleet-agent` sleeps on its own, keep its log
and the OS record of that sleep (`pmset -g log` on macOS, the `PM: suspend exit`
journal line on Linux), and post them on #132. Never put a machine to sleep for
this.

Build `wakenet` as in [Commands run](#commands-run-all-rc0), copy it to the
laptop as `/tmp/wakenet` (`scp` adds no quarantine flag; if a binary arrives
another way on macOS, run `xattr -d com.apple.quarantine /tmp/wakenet` once),
and leave it running inside tmux:

```bash
/tmp/wakenet -interval 5s -duration 48h | tee /tmp/wakenet-wake.jsonl   # add -route-exec on macOS
# after a sleep and wake happened on their own:
pmset -g log | rg " (Sleep|Wake|DarkWake) " | tail -8                    # macOS
sysctl kern.waketime kern.sleeptime                                      # macOS
journalctl -k -b -o short-iso-precise | rg "PM: suspend (entry|exit)"   # Linux
cat /sys/power/mem_sleep                                                 # Linux: the mode in [brackets]
```

The lines that decide it:

- `"event":"wake_suspected"` (clock) and `"event":"wake"` (sleepmark), with
  `latency_ms` on macOS; on Linux compare their `time` with the journal's
  `PM: suspend exit` line.
- `"event":"net_change"` / `"default_route_change"` (ifpoll) and relevant
  `RTM_*` lines (push), with `since_wake_ms` on macOS: does the push socket see
  the wake as a link or address change, and how soon?
- `"event":"summary"`: CPU cost on that machine.

## Decision (ADR 0029)

[ADR 0029](../../../docs/adr/0029-wake-and-network-change-detection.md) holds
the decision; plan row 4 builds it.

- **Wake (both systems):** one check every 5 s with two signals. Clock: the
  wall clock moved 2 s or more past the monotonic clock (`jump.go:26-35`); a
  wall clock stepped back is logged, never a wake. Sleep record: Linux
  `CLOCK_BOOTTIME − CLOCK_MONOTONIC` grew by 2 s or more; macOS `kern.waketime`
  changed. Product code raises the spike's 500 ms sleep-record tolerance
  (`methods.go:115`) to 2 s. Either signal starts one reconnect per check, even
  while connected (close the stream, reconnect with `CONNECT_REASON_WAKE`).
- **Network change:** the push socket (`PF_ROUTE`, `NETLINK_ROUTE`) is only a
  hint. After a hint, wait until 500 ms pass with no further hint (never more
  than 2 s after the first), then read the interfaces that are up and running,
  their addresses and the default route, and compare with the last reading
  (`ifdiff.go:56-97`). No difference, no action. Not connected: a real
  difference resets the backoff and starts an attempt after the 20 to 120 ms
  random delay (the 5 s minimum between connects still applies). Connected: only
  the loss of the connection's own local address from every interface that is
  up, or a changed default route, reconnects. A netlink `ENOBUFS` counts as a
  hint.
- **Interface poll:** fallback only, every 15 s, only when the push socket
  cannot be opened or stops working; logged once and reported by `doctor`. No
  always-on poll loop (polling costs about 3.5× the push socket here).
- **No exec, no cgo.** The macOS default route must not come from running
  `route` (`sys_darwin.go:35-44` is spike-only): read the routing table through
  `x/sys`, or use default-route push messages (`pushparse.go:68-74`). Row 4
  picks one, with a fixture test. `x/net/route`, `godbus/dbus` and `purego` are
  not allowed without another ADR.
- All of this only makes reconnecting faster. A missed event still falls back
  to transport keepalive (ADR 0004: a 10 s ping, a 20 s timeout), so a missed
  wake costs at most about 30 s, and a false positive costs one reconnect.

## What this does not prove

Wake detection on any real machine; dark-wake behavior (`kern.waketime` on a
macOS Power Nap); whether Linux `s2idle` stops `CLOCK_MONOTONIC`; how late Go's
ticker fires after a wake; Wi-Fi roaming flaps on a laptop, where `awdl0` and
other interfaces may come and go; Linux CPU cost, until the 48 h `summary` lines
are recorded; and what the omarchy probe saw during its 20.6 h log gap.
