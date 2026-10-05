# 0028. mDNS discovery is best effort; answers to ADR 0023's questions

- Status: Accepted
- Date: 2026-10-05
- Deciders: linuxdynasty
- Resolves: [0023](0023-additional-libraries.md) open questions 1 and 2
- Supersedes, in part: [0023](0023-additional-libraries.md) (the exec
  fallback for advertising: `dns-sd` or `avahi-publish-service`)
- Builds on: [0004](0004-agent-connectivity.md) (mDNS discovery),
  [0023](0023-additional-libraries.md) (`hashicorp/mdns`)

## Context

ADR 0023 chose `github.com/hashicorp/mdns` v1.0.7 for finding the hub and left
two questions: does it advertise reliably on a macOS hub next to the system
mDNS responder, and what re-query interval and browse timeout should the agent
use. Plan item 0.7 (issue #52) ran the library; the commands and raw output are
in [spikes/mdns/results/](../../spikes/mdns/results/README.md).

- **Same Mac.** The advertiser opened UDP 5353 next to mDNSResponder with no
  error, macOS's `dns-sd` saw and resolved it, and the Go browser found it 150
  of 150 times (worst first answer 10.3 ms). The browser found a service macOS
  registered 10 of 10 times (worst 83.6 ms).
- **Mac to Linux.** From omarchy (Linux, `ufw` on with "deny incoming",
  `avahi-daemon` holding 5353), the browser found the Mac's service 0 of 100
  times, and a service macOS itself registered 1 of 20 times. The kernel log
  shows the hub's answers dropped: `[UFW BLOCK] ... SPT=5353 DPT=<random>`.
  Other devices' answers, sent to the multicast group, arrived.
- **Why.** The library's browser asks from a random port (`client.go`). RFC 6762
  section 6.7 calls that a "legacy unicast" question and requires a direct reply
  to that port, and `ufw` has no state linking such a reply to a multicast
  question. The library's responder also always replies directly, never to the
  group (`server.go`, `sendResponse`), so a browser that asks from 5353 would
  still get a direct reply. macOS's own responder was dropped the same way.
- **Not run.** A hub on Wi-Fi (the owner chose on 2026-10-05 to skip it; the
  hub can be any laptop, so row 4 checks it in real use), a Linux hub, and
  omarchy with its firewall off (needs root).

## Options

**A firewall that drops direct replies:**

1. A querier that asks from port 5353, such as `pion/mdns/v2`. Not run, and it
   also needs a hub that answers by multicast.
2. Ask avahi on the worker (`avahi-browse` or D-Bus). Not run; the same hub
   change is needed.
3. A firewall rule on each Linux worker. Needs root, which the product never
   uses.
4. Treat mDNS as best effort and rely on the cached and configured addresses
   that A2 already tries.

**Re-query timing:** a fixed 10 s timer; 10 s doubling to 60 s; or one query per
connection attempt, on the reconnect backoff 03 already defines.

## Decision

1. **Question 1: yes, on a wired macOS hub with the interface set.** Without
   `Iface`, `NewServer` drops a failed IPv6 bind without a word and the hub runs
   IPv4 only. Wi-Fi and Linux hubs are open question 2.
2. **Question 2: a 2 s browse timeout and no timer of its own.** The agent sends
   one mDNS query per connection attempt, only after the cached address fails,
   so re-queries follow the reconnect backoff (1 to 60 s, full jitter). That
   backoff already doubles and is jittered, so agents dropped by one hub restart
   do not query in step, and a worker behind a firewall does not query every
   10 s forever. The library sends its question once and never repeats it, so
   a longer timeout cannot recover a lost packet; 2 s only has to outlast one
   slow round trip. A hub at a new address is found within one 60 s backoff
   step plus a cycle, inside the 3 minute `OFFLINE` mark. Values in
   [defaults](../specs/defaults.md); the reset rules in
   [03](../specs/03-workflows.md) A2.
3. **mDNS is best effort (option 4).** A browse that finds nothing is a normal
   result: the agent moves on to its configured addresses in the same attempt
   (A2 order unchanged: cached, mDNS, configured). On a host whose firewall drops
   incoming direct UDP, mDNS usually finds nothing. `fleet-agent enroll` without
   `--hub` that finds no hub exits non-zero, names `--hub` and the likely
   firewall cause, and sends no token or CSR. `fleet-agent doctor` reports when
   mDNS found no hub and gives that cause. The product never reads firewall
   state with root and never adds or changes a firewall rule (option 3 is the
   owner's own choice, outside the product).
4. **Discovery keeps only names ending in `._fleetd._tcp.local.`** (the library
   returns unrelated services). Any address found is still only a candidate
   until TLS verifies it against the pinned CA (02g).
5. **The exec fallback is ruled out.** ADR 0023 kept `dns-sd` or
   `avahi-publish-service` for a hub whose advertising clashed with macOS's
   responder on 5353. That clash did not happen, and the fallback would not fix
   the failure seen: a service registered through macOS's own responder was
   dropped the same way. If a Linux hub cannot bind 5353 next to avahi, that is
   a new finding for row 5 and a new decision; the fallback does not come back
   by default.
6. **Hub side (row 5).** Pass the interface and check each address family's
   bind itself, for `doctor` and telemetry. Advertise under a host name of its
   own (for example `fleetd-<host>.local.`) with explicit addresses, because the
   library's default is the machine's name, which mDNSResponder owns.

## Consequences

- When the hub moves to another laptop, a worker behind such a firewall cannot
  find it on its own; the owner gives that worker the new address.
- No new dependency. `pion/mdns/v2` or avahi would each need a spike and, for a
  library, an ADR 0020 line.
- Plan row 4 proves the best-effort path: a fake browse that finds nothing, then
  the configured address connects (`source=configured`); `enroll` with no
  `--hub` and nothing found exits non-zero and sends no token; stray services
  are dropped; and, live, a Linux worker with its firewall unchanged misses on
  mDNS and connects on the configured address.

## Open questions

1. Automatic discovery through a host firewall. Any fix must get a multicast
   answer (to 224.0.0.251:5353) to the worker: the hub answers or announces by
   multicast (RFC 6762 section 8.3; the current browser already reads the
   group, so the worker may need no change), or a querier that asks from 5353,
   or avahi on the worker, each paired with a hub that answers by multicast. Not
   built in v1; it blocks neither row 4 nor row 5. Whoever takes it on first
   shows found and total from a Linux worker with its firewall unchanged.
2. Does a hub on Wi-Fi, or on Linux next to avahi, advertise reliably? Rows 4
   and 5 check it in real use. If any first answer from a Wi-Fi hub takes over
   1 s, or any query is missed in 50, the 2 s timeout is decided again before
   row 4 merges.
