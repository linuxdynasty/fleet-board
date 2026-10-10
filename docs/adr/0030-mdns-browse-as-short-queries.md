# 0030. An mDNS browse is two short queries, read after each returns

- Status: Proposed
- Date: 2026-10-10
- Deciders: arbiter-fleet ruling for plan row 4 PR 4 (#32); the owner
  accepts it by merging that PR
- Supersedes, in part: [0028](0028-mdns-best-effort.md) (decision 2: "one
  mDNS query per connection attempt" and the argument that a longer timeout
  cannot recover a lost packet)
- Builds on: [0023](0023-additional-libraries.md) (`hashicorp/mdns`),
  [0028](0028-mdns-best-effort.md) (best effort, the 2 s budget, one browse
  per attempt on the backoff)

## Context

ADR 0028 decided one 2 s mDNS query per connection attempt. Building it
(plan row 4, PR 4) showed that `github.com/hashicorp/mdns` v1.0.7 cannot be
read that way. Its query loop hands a `*ServiceEntry` to the caller as soon
as the entry is complete (`client.go` lines 376 to 383), and keeps writing the
same entry's fields (host, port, IPv4 and IPv6 address) whenever a later
packet names it (lines 331 to 367), until the query's timeout returns (lines
394 to 395). Reading an entry while its query still runs is a data race on
input from the network: a string or address may be read half written. v1.0.7
is the latest release.

After the query returns nothing writes the entry, so reading it then is safe.
A single 2 s query read after it returns would make every answer wait the full
2 s. 03 A2 asks for each answer to be tried as it arrives.

A reply on Wi-Fi can be slow: a sleeping radio (power save) can delay it past
half a second. A browse made only of short queries could miss it if each
query ends before the reply comes.

## Options

1. **One 2 s query, read after it returns.** Safe, but the first answer always
   waits 2 s, and so does every attempt that finds the hub by mDNS.
2. **Patch or fork the library** so it copies an entry before handing it
   over. A fork to maintain for one copy; ADR 0020 wants no forks of small
   libraries.
3. **`pion/mdns/v2`.** Not evaluated (ADR 0023 and 0028 left it as an
   option); a new dependency and a spike.
4. **Several short queries inside the 2 s, each read after it returns.** Safe,
   with the first answer late by at most one query.

## Decision

Option 4, with two queries: 500 ms, then 1500 ms (2 s in all, the budget of
[defaults](../specs/defaults.md) "mDNS browse timeout").

1. **A browse is two queries.** Each is a fresh library client from a random
   port. Each query's answers are read and handed over only after that query
   returned. The first query hands a quick answer over within 0.5 s; the
   second, longer one still catches a reply slower than 0.5 s (Wi-Fi power
   save). An answer seen in both is handed over once.
2. **Unchanged from 0028:** one browse per attempt, sent only after the cached
   address failed, on the reconnect backoff (reset by wake, network change
   and connect); a browse that finds nothing moves on to the configured
   addresses in the same attempt; mDNS is best effort.
3. **Bounded time per answer and per attempt.** Each answer gets 3 s for its
   dial and TLS handshake together, and the mDNS part of an attempt (or of an
   enroll search) 8 s; then the configured addresses are tried, so answers
   that accept and never reply cannot hold an attempt up.
4. **On macOS the browse asks over IPv4 only** (spike 0.7 finding 3).

## Consequences

- About 4 multicast packets per attempt that browses (two queries, IPv4 and,
  off macOS, IPv6) instead of 2; still only when the cached address failed,
  and on the backoff.
- The first answer is handed over up to one query late: within 0.5 s for a
  reply that comes in the first query, within 2 s otherwise. The "first
  answer over 1 s" bar of ADR 0028 open question 2 is measured on the hand-
  over, which can add up to one query to the reply's arrival.
- The three live checks of plan row 4 (the Wi-Fi hub's 50 browses, advertise
  and discover on a macOS hub, a Linux worker with its firewall unchanged
  connecting on the configured address) run in row 5, once the hub
  advertises and accepts `Connect`: #154.
- If a later release of the library copies entries before handing them over,
  a single query read as answers arrive becomes possible again; that is a new
  decision.

## Open questions

None.
