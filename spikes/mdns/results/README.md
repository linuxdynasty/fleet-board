# Spike 0.7: `hashicorp/mdns` next to macOS mDNSResponder

Plan item 0.7, issue #52. Question: can `github.com/hashicorp/mdns` v1.0.7
advertise `_fleetd._tcp` from a Mac that also runs the system mDNS responder
(mDNSResponder), can a browser find it, and what re-query interval and browse
timeout should the agent use (ADR 0023 open questions 1 and 2, defaults.md row
"mDNS re-query interval ... browse timeout").

**Outcome:** [ADR 0028](../../../docs/adr/0028-mdns-best-effort.md) records the decisions. **Same machine:** yes. The advertiser opens UDP
port 5353 next to mDNSResponder with no error, macOS's own `dns-sd` sees and
resolves it, and the Go browser found it in 150 of 150 tries (median under
1 ms). The Go browser also finds a service macOS registered (10 of 10). There
was no permission prompt and no packet error. **This is weaker than a
cross-machine test**: both ends were on one Mac, so no packet crossed Wi-Fi or
the wire, Linux was not involved, and no firewall was in the path (the macOS
firewall is off on this machine). The owner run below settles that.

**Cross-machine update ([cross-machine-omarchy.md](cross-machine-omarchy.md)):**
from omarchy (Linux, `ufw` active) the browser found the Mac mini's service
0 of 100 times. The answers were sent and then dropped by omarchy's firewall
(`[UFW BLOCK] ... SRC=192.168.1.124 ... SPT=5353 DPT=<random>`), because the
browser asks from a random port and gets direct replies. macOS's own responder
was dropped the same way. A Wi-Fi hub run was skipped by owner decision; row 4
confirms the 2 s timeout in real use.

## Setup

- Machine: Mac mini, macOS 27.0.1 (26A434), Apple silicon, wired `en0`
  192.168.1.124, mDNSResponder running (pid 683). Application firewall off
  (`socketfilterfw --getglobalstate`: `Firewall is disabled. (State = 0)`).
- Go 1.27.1, `CGO_ENABLED=0`; `hashicorp/mdns` v1.0.7 with `miekg/dns`
  v1.1.72 and `golang.org/x/net` v0.48.0. Branch `spike/0.7-mdns` from
  `843df01`, 2026-10-05.
- Probe: `spikes/mdns` (its own module). `advertise` runs `NewMDNSService` +
  `NewServer` and logs every question it answers; `browse` runs
  `QueryContext` in a loop and logs one `browse_run` line per query (found or
  not, time to first answer, entries, errors); `summarize -table` turns those
  lines into the tables below. `-dnssd D` runs macOS `dns-sd -B` and `-L`
  before and after the loop as a diagnostic only. All output is JSON lines.
- The advertiser defaults the SRV host name to `<instance>.local.`, not the
  machine's own name, because mDNSResponder already owns the machine's name
  (see finding 5).

Raw logs are in this folder. Browse entries for other services on the home
network were removed from the committed copies (`other_entries` keeps the
count); nothing else was changed.

## Commands and results

Build: `cd spikes/mdns && CGO_ENABLED=0 go build -o /tmp/mdns07/mdns .` (rc=0).

### 1. Advertiser binds 5353 next to mDNSResponder

```bash
./mdns advertise -instance fleet-spike-mini -iface en0 -stats 60s
```

```json
{"event":"bind_check","group":"224.0.0.251:5353","local":"0.0.0.0:5353","network":"udp4","ok":true}
{"event":"bind_check","group":"[ff02::fb]:5353","local":"[::]:5353","network":"udp6","ok":true}
{"event":"server_started"}
```

No error from `NewMDNSService` or `NewServer`; no `lib_log` line from the
server in any run. Over the 7-minute run it heard 7,743 mDNS questions from
the network and answered 205 (all for `_fleetd._tcp.local.` or the service
list `_services._dns-sd._udp.local.`): `same-machine-advertise.jsonl`.

Without `-iface` the IPv6 bind fails and the library silently runs IPv4 only
(`default-iface-advertise.jsonl`):

```json
{"error":"listen udp6 [ff02::fb]:5353: setsockopt: can't assign requested address","event":"bind_check","network":"udp6","ok":false}
```

### 2. macOS sees the Go advertiser

`browse -dnssd 4s` ran `dns-sd -B _fleetd._tcp local.` and
`dns-sd -L fleet-spike-mini _fleetd._tcp local.` before and after each of the
three 50-run loops (6 of each) and once in each default-interface run. All 8
`-B` runs saw the instance (`"instance_seen":true`) on `en0` (if 7) and `ap1`
(if 15); all `-L` runs resolved it with the TXT record:

```text
10:58:32.131  fleet-spike-mini._fleetd._tcp.local. can be reached at fleet-spike-mini.local.:47999 (interface 15) Flags: 1
 txtvers=1 role=spike
```

### 3. Go browser finds the Go advertiser (two processes, one Mac)

```bash
./mdns advertise -instance fleet-spike-mini -iface en0 -stats 60s &
for t in 1 2 3; do
  ./mdns browse -instance fleet-spike-mini -iface en0 -timeout ${t}s \
    -interval $((t * 1000 + 500))ms -count 50 -dnssd 4s > br-dist-${t}s.jsonl
done
./mdns summarize -table br-dist-1s.jsonl br-dist-2s.jsonl br-dist-3s.jsonl
```

| timeout | runs | found | rate | errors | first answer ms: min | p50 | p90 | p99 | max |
|---|---|---|---|---|---|---|---|---|---|
| 1000 ms | 50 | 50 | 1.00 | 0 | 0.5 | 0.7 | 2.8 | 10.3 | 10.3 |
| 2000 ms | 50 | 50 | 1.00 | 0 | 0.4 | 0.9 | 2.8 | 4.3 | 4.3 |
| 3000 ms | 50 | 50 | 1.00 | 0 | 0.4 | 0.6 | 1.3 | 4.1 | 4.1 |

Each browse rc=0. "First answer" is from the `QueryContext` call to the
first complete entry for our instance, so it includes opening the sockets.

Default interface on both sides (no `-iface`), 1 s timeout:

| browser | runs | found | errors | first answer ms: min | p50 | p90 | p99 | max |
|---|---|---|---|---|---|---|---|---|
| default | 20 | 20 | 0 | 2.2 | 2.8 | 3.9 | 5.1 | 5.1 |
| `-no-v6` | 20 | 20 | 0 | 1.3 | 2.2 | 2.7 | 2.9 | 2.9 |

### 4. Go browser finds a service macOS registered (reverse check)

```bash
dns-sd -R test _fleetd._tcp local. 9999 &      # "Name now registered and active"
./mdns browse -instance test -iface en0 -timeout 2s -interval 2.5s -count 10
```

| timeout | runs | found | errors | first answer ms: min | p50 | p90 | p99 | max |
|---|---|---|---|---|---|---|---|---|
| 2000 ms | 10 | 10 | 0 | 2.2 | 3.6 | 35.7 | 83.6 | 83.6 |

### 5. Errors seen, verbatim

Every browse query on macOS logs library lines, then works over IPv4: both
lines in all 180 committed runs without `-no-v6`, only the `[INFO]` line in all
20 runs with it:

```text
[ERR] mdns: Failed to bind to udp6 port: listen udp6 [ff02::fb]:5353: setsockopt: can't assign requested address
[INFO] mdns: Failed to listen to both unicast and multicast on IPv6
```

Probe default bug, fixed before the runs above: with no `-iface` and no
`-ip`, a made-up host name cannot be looked up and the advertiser stopped after
10 s with `new service: could not determine host IP addresses for
fleet-spike-noif.local.`

No Local Network permission dialog was needed: multicast sends worked from
the first run (other devices on the network answered the browser too). No
`sudo`, no network setting changed, no other machine touched.

## Findings for PR 4 and PR 5

1. **No conflict with mDNSResponder on this Mac.** Both share 5353 (Go sets
   `SO_REUSEADDR` and `SO_REUSEPORT` on multicast sockets), mDNSResponder kept
   answering, and each side saw the other's service.
2. **`NewServer` hides a failed family.** It drops a per-family bind error and
   fails only if both fail. On macOS with no `Iface`, IPv6 fails and the hub
   would run IPv4-only without a word. The hub should pass the interface (or
   accept IPv4-only) and check the bind itself for `doctor` and telemetry.
3. **The browser always tries IPv6 without an interface**, so it logs `[ERR]`
   on every query on macOS. Use `DisableIPv6: true` on macOS (one `[INFO]`
   line remains) and send library log lines to debug, not error.
4. **The browser returns other services.** It does not check that an answer
   matches the question (a TODO in `client.go`), so `_sleep-proxy._udp` and
   other entries arrived during `_fleetd._tcp` browses. Discovery must keep only
   names ending in `._fleetd._tcp.local.`; TLS against the pinned CA still
   decides (02g).
5. **Host name.** The library's default SRV host is the machine's own name,
   which mDNSResponder owns, so the hub would answer address queries for the
   OS's name. Use a distinct name (for example `fleetd-<host>.local.`) and pass
   the addresses explicitly, because the library cannot look such a name up.
6. **Replies are always unicast** to the asker's source address and port, even
   for multicast questions (`sendResponse` TODO in `server.go`). It never
   announces and sends no goodbye. Unicast replies to a random port are the kind
   a host firewall drops (for example a Linux `ufw` with "deny incoming"
   allows only packets *to* 224.0.0.251:5353). Not tested: no firewall here.
7. A `ServiceEntry` can still be written by the library after it is sent;
   copy its fields on receipt (the probe does).

## Proposed values (superseded by ADR 0028)

ADR 0028 kept the 2 s browse timeout and dropped the separate 10 s timer: the
agent sends one query per connection attempt on the reconnect backoff. The
proposal as written before that ruling:

- **Browse timeout: 2 s.** Same-machine answers arrive in 10.3 ms at worst
  for the Go advertiser and 83.6 ms for mDNSResponder. Across Wi-Fi, multicast
  waits for the access point's beacon (about 100 ms, times the DTIM period of
  1 to 3) and the sleeping laptop radio, so a few hundred ms is a plausible
  worst case. The library sends one query and never repeats it, so a longer
  timeout cannot recover a lost packet; it only has to outlast one slow round
  trip. 2 s is several times that worst case; 1 s, 2 s and 3 s all found 50 of 50.
- **Re-query interval: 10 s**, start to start, only while the agent has no
  connection (cached address first, A2). A lost packet then costs one interval;
  a hub with a new address is found in about 12 s at worst, well inside the
  45 s STALE mark. Cost is one small query and a few answers per 10 s per
  disconnected agent. Note: RFC 6762 section 5.2 asks continuous queriers to
  double the gap between queries; if the arbiter wants that, use 10 s doubling
  to a 60 s cap instead.

## Exec fallback

ADR 0023 keeps the exec fallback (`dns-sd` or `avahi-publish-service`) for
the case where the hub's advertisement "conflicts with macOS's own mDNS
responder on port 5353". On this Mac it did not: same-machine evidence
supports ruling the fallback out for that reason. It is **not ruled out yet**:
the cross-machine run has to show the Wi-Fi hub reaches a Linux browser.
If it fails only on Linux because of a firewall (finding 6), the fix is on
the responder side (`brutella/dnssd`, ADR 0023's first fallback, answers by
multicast) or a firewall rule, and the hub-side `dns-sd -R` control in step 4
below tells which.

Update: the cross-machine run hit exactly that firewall case, and the step 4
control showed mDNSResponder's answers dropped too, so a responder swap does
not fix it; the gap is on the browse side. The options are in
[cross-machine-omarchy.md](cross-machine-omarchy.md#what-it-means). ADR 0028
rules the exec fallback out and makes mDNS best effort.

## ADR 0023 status

ADR 0023 is **Accepted** (2026-10-02), so its open questions cannot be edited
in place (AGENTS.md, ADR index). The repo's pattern is that a later ADR
answers an earlier one's questions (0022 answers 0021; 0024 answers 0022) and
the index's Relations cell says so. ADR 0028 answers both, and the 0023 index row
says so.

## Owner run

Done in part on 2026-10-05: the Mac mini advertised and omarchy browsed, over
SSH with the owner's permission and no `sudo`
([cross-machine-omarchy.md](cross-machine-omarchy.md)). The owner chose to skip
the Wi-Fi hub run; plan row 4 checks it in real use (ADR 0028 open question 2).
