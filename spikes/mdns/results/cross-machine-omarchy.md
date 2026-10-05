# Cross-machine run: Mac mini advertises, omarchy browses

2026-10-05, run by the coordinator over SSH (owner-authorized, no sudo).
There is no Wi-Fi hub run: the owner chose on 2026-10-05 to skip it, keep the
proposed 2 s browse timeout, and let row 4 confirm it in real use (the hub can
be any laptop, so no one machine is special).

## Setup

- Advertiser: Mac mini, macOS 27.0.1, wired `en0` 192.168.1.124,
  mDNSResponder running. `mdns advertise -instance fleet-spike-x -iface en0
  -duration 10m -stats 60s`. Both bind checks ok (udp4 and udp6 on 5353).
- Browser: omarchy, Linux 7.2.5 x86_64, wired `enp3s0` 192.168.1.150,
  `avahi-daemon` active and holding 5353, `ufw` active. Binary built with
  `CGO_ENABLED=0 GOOS=linux GOARCH=amd64` from this branch (sha256 prefix
  `58c14e556b252dea`, same on both ends). omarchy's `/tmp` is over the user's
  disk quota (`cat: write error: Disk quota exceeded`), so the binary ran from
  `~/fleet-spike/`, deleted afterwards.

## Results

| Browse | runs | found | errors |
|---|---|---|---|
| Go browser to Go advertiser, 1 s timeout, 1.5 s interval | 50 | 0 | 0 |
| Go browser to Go advertiser, 2 s timeout, 2.5 s interval | 50 | 0 | 0 |
| Go browser to a service macOS registered (`dns-sd -R fleet-spike-os _fleetd._tcp local. 47998`), 2 s | 20 | 1 (at 799.8 ms) | 0 |

- The browser did receive mDNS traffic: other devices' services (a TV, a
  smart-home hub, the Mac mini's own `_companion-link`) appear in its
  `entries`, first one at 69.6 ms. Those are replies sent to the multicast
  group.
- The Go advertiser heard the questions and answered: 245 answers for
  `_fleetd._tcp.local.` PTR over the run (8,559 questions heard in total).
- omarchy's kernel log (`journalctl -k`, readable without sudo) shows the
  answers being dropped, 19 logged lines in the window (ufw rate-limits its
  log), for example:

```text
Oct 05 13:33:50 omarchy kernel: [UFW BLOCK] IN=enp3s0 OUT= SRC=192.168.1.124 DST=192.168.1.150 LEN=202 TOS=0x00 PREC=0x00 TTL=64 ID=727 PROTO=UDP SPT=5353 DPT=34582 LEN=182
```

## What it means

- **Cause (observed):** the `hashicorp/mdns` browser sends its question from a
  random port. RFC 6762 section 6.7 treats a question from a port other than
  5353 as a "legacy unicast" query and requires a direct (unicast) reply to
  that port. ufw's default "deny incoming" allows multicast to
  224.0.0.251:5353 but has no state that links a direct reply to a multicast
  question, so it drops the reply.
- **The responder is not the problem.** macOS's own mDNSResponder was dropped
  the same way: its one hit is most likely the multicast announcement it sends
  when a service is registered, not an answer to a question. So switching the
  hub to another responder library (`brutella/dnssd`) would not help.
- **The advertise side on macOS works** next to mDNSResponder (both bind checks
  ok, answers sent), which answers ADR 0023 question 1 for a wired hub.
- **The browse side on Linux needs one of** (a design choice for PR 4, not made
  here):
  1. a pure-Go querier that sends from port 5353 so answers come back by
     multicast (for example `pion/mdns/v2`, listed but not evaluated in ADR
     0023), sharing the port with avahi;
  2. asking avahi when it runs (`avahi-browse` exec, or its D-Bus API), which
     is the exec fallback ADR 0023 keeps;
  3. a firewall rule the owner adds on each Linux worker;
  4. relying on the cached or configured hub address (A2) and treating mDNS as
     best effort.
- **The exec fallback is therefore not ruled out**, but for a different reason
  than ADR 0023 gave: the gap is on the browse side behind a host firewall, not
  a 5353 conflict on the macOS hub.

## Not checked

- A hub on Wi-Fi (beacon and DTIM delays on the proposed 2 s timeout):
  skipped by owner decision; row 4 confirms it in real use.
- omarchy with ufw off or with an allow rule (needs sudo; would confirm the fix
  for option 3 and give cross-machine timing).
- Whether a query sent from 5353 really gets a multicast answer from the Go
  advertiser: the library always replies unicast (finding 6 in README.md), so
  option 1 also needs the hub to answer by multicast.
