# internal/agent/conn

The agent's link to the hub: enrollment
([02g A1](../../../docs/specs/02g-agent-security.md),
[03 W1](../../../docs/specs/03-workflows.md#w1-enrollment-and-first-connect)),
run by `fleet-agent enroll [--hub <host[:port]>] --token <token>`, and the
connection loop (03 W1 "State machine", 03n W2), run by `fleet-agent run`.

```go
res, err := conn.Enroll(ctx, conn.Options{Dir: dir, Hub: "hub.local", Token: tok, Version: v, Telemetry: tel,
	Discover: conn.NewMDNS()}) // Hub "" browses; ErrNoHubFound when no answer holds the token's CA
id, err := conn.LoadIdentity(dir)  // ErrNotEnrolled when there is none
addr, err := conn.LoadHubAddress(dir)
removed, err := conn.CheckEnded(dir, id) // *EndedError: the hub ended this machine; do not dial
l, err := conn.NewLoop(ctx, conn.LoopOptions{Dir: dir, Identity: id, DB: db, Version: v, BootID: b,
	Telemetry: tel, Events: wakeAndNet, PushOff: watcher.PushOff, Heartbeat: fillLaterFields,
	Discover: conn.NewMDNS()})
err = l.Run(ctx)                    // nil after ctx ends; *EndedError when it ended on its own
st := l.Status()                    // state, peer, last failure per address, last browse, clock offset, ...
```

`Enroll`, in order, stopping at the first failure:

1. **Nothing leaves before the local checks.** The token must parse
   (`ErrTokenMalformed`), a hub address must be given or `Discover` set
   (`ErrNoHub` with neither), and the state folder must be private.
   `HubAddress` spells an address one way (lower case, port 7443 added when
   missing, canonical IP) and refuses an IPv6 zone or more than 261 bytes;
   the hub's `fleetd restore --address` uses the same rules. This is a
   change: a zoned IPv6 address (`[fe80::1%eth0]:7443`) the agent used to
   keep is now refused, so such an entry in `hub-address`, `hub-addresses`
   or `identity.json` is dropped like any invalid line and the agent moves
   to its next candidate. A zone means nothing off the local link, and it
   could carry any byte. Discovery is not affected: an mDNS answer's
   link-local address is dialed as given; only its SRV name goes through
   `HubAddress`.
2. **Never replace an identity.** Until re-enroll exists (later in plan row
   4, and row 13a's `previous_machine`), an enrolled folder is refused
   (`ErrAlreadyEnrolled`, naming the machine and saying to remove only
   `identity.json` and keep `agent.db`, and that enroll then removes
   `cert.pem` and `renew.key`) before any network call, and a leftover
   `enroll.key` is removed. The identity file is also written with a hard
   link, which cannot overwrite.
3. **Same key on a retry.** The Ed25519 key is kept in `enroll.key` (0600)
   until Enroll succeeds, and `install_id` comes from `agent.db`, so a retry
   or a second run after a lost reply gets the hub's stored answer
   (`Result.Replayed`), not a second machine.
4. **Pinned TLS, no client certificate.** The handshake passes only if the
   hub's chain holds the CA whose SHA-256 is in the token, its certificate
   chains to that CA for the address's host, and it proves it holds that
   certificate's key. A handshake the peer fails (the pin, the name, the key
   proof, or a non-TLS answer) is untrusted and never retried
   (`ErrHubUntrusted`), before the request is written: an impostor gets
   neither the token nor the CSR. A connection that drops, resets or times
   out during the handshake is a lost connection and is retried within
   "Enroll tries". No system roots and no proxy are used.
5. **Bounded retries.** A retryable hub answer, a bare `UNAVAILABLE`,
   `UNKNOWN`, `INTERNAL`, `DEADLINE_EXCEEDED`, `ABORTED` or Connect "canceled" (a cut stream:
   connect-go reports an HTTP/2 RST_STREAM with CANCEL that way, as when the
   hub stops, its own timer wins or a proxy cuts it), or
   a lost connection is tried again with the same request
   (`DefaultAttempts`, full-jitter waits, defaults.md "Enroll tries"); the
   whole enroll is capped at `DefaultTimeout`. Only a non-retryable
   `ErrorDetail` reason is a `*RefusedError`. Tries run out, or the time
   cap ending a call or a wait, is `ErrHubFailed` saying a rerun is safe
   (the caller's own cancel during a call gets no advice); an `INTERNAL` with the hub's
   `ErrorDetail`, or `RESOURCE_EXHAUSTED` (the 1 MiB cap), is `ErrHubFailed`
   with no rerun advice, since a rerun fails the same way.
6. **Check, then save.** The response must carry the pinned CA and
   fingerprint, one client certificate for this key that chains to the CA and
   names the machine id (common name and `fleet://machine/<id>`), a fleet id
   and a salt (`ErrBadResponse` otherwise; nothing saved, key kept).
7. A leftover `cert.pem` and `renew.key` (the removed identity's renewal)
   are removed, the hub address goes to `hub-address`, the identity to
   `identity.json` (temp file, fsync, link, folder fsync), a leftover
   `ended.json` and `hub-addresses` are removed, then `enroll.key`.
   `Result.HostnameMismatch` says the hub expected another hostname (the
   rule is `internal/hostname`, shared with the hub); the command prints a
   warning for it.

**With no hub address** (03 A2, ADR 0028), after step 2 and before any
key or request: one mDNS browse (`Discover`, `DefaultBrowseTimeout`). Each
answer named `*._fleetd._tcp.local.` with a valid port, a target that is a
host name other than this machine's, and another machine's unicast address
is a candidate as it arrives (16 at most; the rest are dropped by reason):
it gets only the handshake of step 4, pinned to the token's CA for the SRV
target's name, at the answer's address, within 3 s for the dial and the
handshake together, and the whole search stops after 8 s. The first that
passes is used for the rest (`Result.Discovered`, `enroll.discovered`); one
that fails it or cannot be reached gets nothing more. None is
`ErrNoHubFound` (`enroll.failed{step="discover"}`), naming `--hub` and what
was seen: nothing answered (`timeout`) or the browse could not run (`io`),
with the likely firewall cause; answers that did not prove the CA or could
not be reached (`refused`, or `timeout` when not every one refused the
connection), without it. The whole-enroll cap or a stop during the search
says so instead (`ErrHubFailed` or the stop), with no advice. Nothing was
sent.

## Connection loop

`Loop.Run` repeats one attempt until ctx ends:

1. **Certificate first.** Past the end of the certificate in use (see
   "Certificate renewal"; not `Identity.CertNotAfter`) the loop ends
   (`EndCertExpired`) without dialing.
2. **Candidates.** The cached `hub-address`; then, after it failed, one
   mDNS browse (`LoopOptions.Discover`; answers kept as for enroll, each
   tried as it arrives, dialed at its address within 3 s for the dial and
   handshake together, and named, logged and cached by its SRV target; the
   mDNS part stops after 8 s, `connect.mdns_cut`); then the configured
   addresses, each once. The
   configured list is `hub-addresses` (the hub's latest) when it holds a
   valid line, else the identity's (`connect.addresses{source}` says which);
   a HelloAck list that is non-empty and differs from it replaces it and is
   saved whole to `hub-addresses`, then used (a failed save logs
   `connect.failed{step="save"}`, the stream stays up and the old list
   stays in use, so the next HelloAck tries the save again). A list drops
   invalid entries and repeats (names in lower case), then keeps 16. One whose handshake fails (refused, timed out after
   `LoopOptions.HandshakeTimeout`, 10 s by default, or untrusted: the same
   pin as enroll) is skipped for the next, logged at `step="dial"`; the
   first that passes is used. The client certificate is chosen at each
   handshake (see "Certificate renewal").
3. **Hello, HelloAck.** Hello carries the marks from `agent.db`
   (`last_epoch`, `last_applied_command_seq`, `last_event_seq` = `seq_hwm`,
   counters, `install_id`, `boot_id`, the reason). No HelloAck within
   `DefaultHelloTimeout` (30 s) is a failure. Another fleet's `fleet_id`
   gets `Bye{FLEET_MISMATCH}` and ends the loop, before anything is saved;
   an epoch above `ring.MaxEpoch` or a `hub_last_event_seq` above
   `ring.MaxHubLastSeq` (too large to leave room for the next epoch or seq
   block in `agent.db`, which never goes back) saves neither and is
   `connect.failed{step="hello", err_kind="constraint"}` and a backoff; an
   epoch not above the last is `connect.epoch_regressed` and a backoff;
   otherwise the epoch is
   stored, the next seq floored above `hub_last_event_seq` (and saved as
   `seq_hwm` when above it, so a failed snapshot does not lose it), and the
   clock offset kept.
4. **Snapshot, then results.** Each stream builds its own snapshot (new
   `snapshot_id`, a fresh seq taken before it is built, because a snapshot
   supersedes the events at or below its seq; the facts stub with `boot_id`, the unacked
   result ids; `LoopOptions.Snapshot` fills the rest when later rows add
   it). If the stream ends while it is built or sent, it is dropped, never
   sent on the next stream. Over 960 KiB it is not sent
   (`snapshot.oversize`; parts are not built yet). Each finished result
   without a `ResultAck` follows, oldest first; the command log is
   pruned first (`ring.DB.Prune`: 7 days, and 10,000 entries without ever
   dropping one the hub may re-send). The count cap is soft by design: an
   unacked entry inside its deadline is never pruned for the count, even
   past 10,000, because a forgotten `command_id` the hub re-sends would
   run twice (02i "What agent.db keeps", defaults.md); the 7-day age limit
   likewise spares an entry the hub may re-send. It prunes by hub time
   (this stream's `HelloAck.hub_time` plus the time elapsed since the
   HelloAck arrived, read from the monotonic clock, 02i "Clocks"), never
   the agent's wall clock: `not_after_time` is the hub's deadline, and
   setting the wall clock does not move hub time. A HelloAck with no
   `hub_time` falls back to the agent's clock at its arrival and logs
   `hub_time_ok=false` (skew 0, not measured); a conforming hub always
   sends it. Hub time this way does not count time asleep (the monotonic
   clock stops or slows in sleep), so it runs late after a sleep: safe
   for pruning, which then happens later, but row 6's executor must not
   reuse it for deadline checks, which must count the time asleep.
5. **Connected.** The cached address is rewritten if it changed. A
   `ResultAck` stamps its ids (unknown ones change nothing); a
   `SnapshotRequest` gets a new snapshot and the unacked results; a `Bye`
   other than `REVOKED` or `UNKNOWN_MACHINE` closes the stream; `Command`
   and `Settings` go to their hooks (see "Commands and the outbound
   queue"), or are logged as `connect.message_ignored` with none. On stop the
   agent sends `Bye{SHUTDOWN}` and waits up to 2 s for the hub to close;
   then the stream and its connection are cut (`connect.shutdown_cut`), so a send held by HTTP/2
   flow control (a hub that stopped reading) cannot hold up the stop. A
   stop before Connected cuts at once and is not a failed attempt.
   **Heartbeat (A5).** Every `HelloAck.heartbeat_interval` when it is 1 to
   15 s, else 15 s (logged as `connect.hello{heartbeat_ms}`), the first one
   interval after the snapshot and its results: `sent_time`, `uptime`
   (monotonic, since the process started), `event_seq` (`ring.Seqs.Last`)
   and `events` (agent.db's counters, as in Hello), set after the
   `LoopOptions.Heartbeat` hook, which fills what later rows own. It is a
   `time.Ticker` case in the same select, so the one sender never puts it
   between a snapshot and its results, and a tick missed while a send
   waits is dropped. A failed read or send closes the stream
   (`connect.closed{step="heartbeat"}`).
6. **Backoff.** After n failures in a row, full jitter from 1 s up to
   2^n s (the first wait is 1 to 2 s), capped at 60 s; a connect resets n,
   and so does a wake or network change, whether it comes in the backoff
   wait or in the 5 s gap between tries (one `pacer` holds the rule for
   both); each wait is logged as `connect.backoff{failures, wait_ms,
   reason}`. A wake read in the gap starts its try when the gap ends,
   without the 20 to 120 ms random delay a wake in the backoff wait gets
   (accepted: the gap has already spread the tries); attempts at least 5 s apart. While connected, a
   wake, or a network change that removed the connection's local address
   or changed the default route (`wakenet.Decide`), closes the stream; in
   backoff, a wake or any network change cuts the wait. Either resets the
   backoff and reconnects after 20 to 120 ms with that reason.
7. **Keepalive.** HTTP/2 ping after 10 s with nothing received, closed 20 s
   later without an answer.
8. **The end.** `Bye{REVOKED}`, `Bye{UNKNOWN_MACHINE}` or another fleet's
   `HelloAck` is written to `ended.json` (`version`, `end`, `machine_id`,
   `peer`, `time`; temp file, fsync, rename, folder fsync) before `Run`
   returns the `*EndedError`; a failed write logs `connect.failed{step="save"}`
   and the loop still ends. An expired certificate is not recorded: it is
   read from the certificate. `CheckEnded`, called by `fleet-agent run`
   before any network call, returns a `*EndedError` (`Recorded`, its message
   naming the hub and the owner's fix) while the file names this machine,
   removes one that names another machine (a leftover), and refuses a
   damaged one (`ErrEndedDamaged`). Enroll removes it when it saves a new
   identity.

## Commands and the outbound queue

Plan row 6's seam (ruling forks 4 i, 11, 12); the rules are in 03n
"Commands, settings and the outbound queue". Files: `inbound.go` (the
`Command` and `Settings` hooks, capabilities, `connect.hook_blocked`),
`outbound.go` (`Loop.Send`, the per-stream queue, `ErrNoStream`,
`ErrNotQueueable`, `ErrResultNotLogged`, `ErrQueueFull`, the unlogged
refusals, the flush before a requested snapshot, `OnConnected` and
`OnDisconnected`), `hubclock.go` (`Loop.HubClock`, the strict deadline
clock of 02i "Clocks"). For code that plugs in (6e, 6f): hooks run on the
sending goroutine and must not block; Send from a hook never waits (with
the hook's context a full queue is `ErrQueueFull`), so check its error; a
goroutine a hook starts uses its own context, which keeps the bounded
wait; a result is sent only once logged (proto-equal); `Connected` in the
status means Send takes messages. Known limit: the receiver reads 8
messages ahead, so a hook that never returns while the hub sends more than
8 is released only by the run's end (`connect.hook_blocked` says so). A
Bye read ahead of the stream's end is acted on first (`streamend.go`).

Tests: `seam_test.go`, `outbound_test.go`, `outboundhooks_test.go`,
`strictclock_test.go` (each test's comment names the rule it pins).

## Certificate renewal

02g "Certificate renewal (A4)" (with the arbiter's amendment), 03 W1.
The files are `certpair.go` (a pair and its checks), `pairset.go` (which
pair to present, which to renew, hub time), `renewgate.go` (when a due
renewal may start), `renew.go` (the steps), `renewlog.go` (their logs) and
`renewcheck.go` (the answer).

- **The pairs.** `NewLoop` takes the enrolled pair from `identity.json`
  and the one in `cert.pem` (one CERTIFICATE block, then one PKCS#8
  Ed25519 PRIVATE KEY block, 0600) when it is valid: it parses, the key
  matches, it chains to the pinned CA with client-auth usage, names the
  machine id, and ends no later than the CA. A `cert.pem` that is not
  valid is logged (`cert.file_invalid{file, check, err_kind}`), never
  removed or changed at start (row 6's doctor reports it), and the next
  renewal replaces it. A `renew.key` holding the valid `cert.pem`'s key is
  a leftover of a saved renewal and is removed (`renew.leftover_removed`);
  any other is kept for the next try.
- **Which pair.** Two decisions, split: each TLS handshake presents, of
  the pairs inside their life by the agent's clock, the one that ends
  later (the lead, below, when neither is), so a renewed pair signed by a
  hub clock ahead of the agent's is not presented before it starts while
  the enrolled one is good. The renewal check, the loop's expiry check and
  `fleet_agent_cert_expiry_seconds` follow the lead, the pair that ends
  later whether it has started or not, so a not-yet-started pair starts
  no new renewal. Those three run on hub time (the last `HelloAck`'s
  `hub_time` plus the time since, the larger of Go's monotonic clock and
  `wakenet.SinceBoot`, which keeps counting while the machine sleeps,
  kept on the loop; the agent's
  clock before the first `HelloAck` or after one with no `hub_time`), so
  an agent clock days off neither renews at every check nor ends the loop
  while the hub takes the pair; only the pair to present runs on the
  agent's clock. When an attempt that presented one pair fails before
  Connected, the next presents the other while it is inside its life
  (`renew.pair_switched`), until a stream connects; backoff is unchanged,
  and nothing is read into the refusal. The set is an atomic pointer that
  `GetClientCertificate` reads at each handshake, so a swap reaches the
  next connection while the open stream keeps its own. There is no TLS
  session cache, so no resumed session hides the new certificate.
- **When.** At two thirds of the lead's life (`NotBefore` + 2/3 of its
  span: day 60 of 90), checked once each stream is connected (after its
  `HelloAck` and snapshot) and every `LoopOptions.RenewCheck` (1 h) while
  connected. A renewal does not start within one check interval of the
  last, nor for a lead that ends at the CA's end, nor for a lead already
  due by hub time when it was saved until a later check finds it not due
  (`renew.skipped{reason}`, once per lead; see `renewgate.go`). One renewal runs at a time, on a goroutine the session owns;
  it is cancelled and waited for before the stream's connection closes,
  and it never sends on the stream. `RenewCert` is a unary call on the
  stream's own HTTP/2 connection (a test proves the reuse).
- **Steps.** A new Ed25519 key is written to `renew.key` (0600, never over
  another file) before anything is sent; a later try reuses it, so the
  hub returns the certificate it stored for that key (a `renew.key` with
  the key in use is replaced: the hub would refuse it). The answer's
  certificate must fit in `cert.pem` (64 KiB with its key), and the
  answer must carry exactly the pinned CA as its chain and one
  certificate for `renew.key`'s key that chains to it, names the machine
  id, ends no later than the CA and after the lead; the chain is checked
  at a time inside the certificates' own lives, not the agent's clock, so
  an agent clock behind the hub's does not refuse a fresh certificate.
  Then `cert.pem` is replaced (temp file, fsync, rename, folder fsync),
  the set swapped, and `renew.key` removed. `identity.json` is never
  touched.
- **Failure.** A failure saves nothing, keeps `renew.key` and the stream,
  and is tried again at the next check: a refusal (`renew.refused`), a
  failed call such as an older hub's `UNIMPLEMENTED` or a busy one's
  `UNAVAILABLE`, an answer that fails a check, or a `cert.pem` that cannot
  be written (`renew.failed{step}`, with `file`, `check` and, when the
  owner must act, `fix` for a file it could not use). A stream that ends
  or a stop during the call logs `renew.cancelled`, not a failure. A
  `renew.key` that is damaged or open to other users is kept, never
  replaced (the ruling keeps any other `renew.key`): each check logs
  `renew.failed{step="key", check, file, fix}` and sends nothing, so
  renewal waits for the owner. The loop never ends for a renewal; if
  renewal keeps failing, a stream that is up stays up past the
  certificate's end (the hub checks it only at a handshake), and the loop
  ends with `EndCertExpired` at its next attempt, without dialing.

Not built yet, all later in plan row 4:

- Local config for hub addresses (03 A2's "plus local config") and the
  Tailscale name (v3).

Known limits of mDNS (best effort, ADR 0028): a host firewall that drops
direct UDP replies (ufw on Linux) hides the hub; a hub's certificate must
name its SRV target and advertise a TXT record (the library hands over
only answers with one), and never a loopback address or name (row 5); the
library returns from a query only when its timeout passes, even after a
stop, so a stop during a browse waits for the query in flight (up to
1.5 s); and it keeps writing an answer it already handed over from later
packets until its query returns, so `NewMDNS` asks a 0.5 s query then a
1.5 s one and reads each answer only after its query returned (ADR 0030:
a quick reply is handed over within 0.5 s, a slow one within 2 s). A stop
that cuts a query's send is a stop, not a failed browse, and a query that
fails after an earlier one kept answers is only logged
(`connect.browse{late_err_kind}`). On macOS
it asks over IPv4 only (spike 0.7 finding 3). On a host that does not let
the process send multicast on its network interface, the query fails at
once (`sendto: no route to host` on the IPv4 send, `connect.browse`
`err_kind="io"`) and the configured addresses are used; this happened on
the development Mac for the agent's process while loopback multicast and
LAN unicast worked.

**State folder** (04 Q9): `~/.local/state/fleet-agent/` unless
`--state-dir`; created 0700, and a folder or file with any group or other
bit, another owner, or a symlink is refused (`ErrLooseMode`), never re-moded.

| File | Holds |
|---|---|
| `identity.json` | machine id and name, fleet id, CA PEM and fingerprint, client certificate and its expiry, key, account salt and version, the hub's configured addresses |
| `hub-address` | last good `host:port` |
| `enroll.key` | the key, only between a first try and success |
| `cert.pem` | a renewed certificate and its key, replaced whole by each renewal; removed by enroll |
| `renew.key` | the key of a renewal in progress, until its certificate is saved; removed by enroll |
| `ended.json` | the end that stopped the loop (revoked, unknown machine, fleet mismatch), its machine id, hub and time; removed by enroll |
| `hub-addresses` | the hub's latest address list (`HelloAck.hub_addresses`), one `host:port` per line, at most 16; read at start ahead of the identity's list; removed by enroll |
| `agent.db` | `install_id` and the rest ([ring](../ring/README.md)) |

**Signals.** Spans `agent.enroll`, `agent.connect.hello`, `agent.discover`
(children `agent.discover.browse` and `agent.discover.candidate`),
`agent.cert.renew`. Loop logs and metrics are listed in 03 W1 and 03n W2
"Telemetry" (`connect.*`, `snapshot.*`, `result.*`, `renew.*`;
`fleet_agent_connect_attempts_total`, `fleet_agent_connected`,
`fleet_agent_reconnects_total`, `fleet_agent_unacked_results`,
`fleet_agent_cert_expiry_seconds` for the pair that ends last,
`fleet_agent_cert_renewals_total{result}`, `fleet_agent_discovery_seconds`),
and `cert.file_invalid` in 05b.
Enroll logs: `enroll.completed{machine_id,
replayed, hostname_mismatch}`, `enroll.refused{reason, peer}`,
`enroll.retry{attempt, err_kind}`, `enroll.hub_untrusted{peer}`,
`enroll.discovered{peer, kept, dropped, elapsed_ms}`,
`enroll.failed{step, err_kind}` with `step`
one of `token`, `hub_address`, `state`, `already_enrolled`, `discover`, `key`, `call`,
`response`, `save`, `cleanup`. No line holds the token, the key, the salt or
the facts; `Token` and `Identity` print redacted in every format.

Tests run against the real hub listener (`conntest`, on a real `state.db`):
enroll end to end with modes and a certificate that chains to the CA; a
token naming another CA refused with zero requests at the hub; five
impostors (one with the hub's real chain but not its key) that receive no
byte; expired and burned tokens; a dropped reply recovered as a replay with
the same certificate, by a second run and within one run; re-enroll after
removing `identity.json` keeping `install_id`; an enrolled folder left
alone and a leftover key removed; no hub and a bad token sending nothing; a
loose folder and symlinks; the hostname check; retries of busy answers and
reset streams with the same request, and refusals, a hub-side `INTERNAL`
and an over-cap request not retried; listeners that close or reset during
the handshake retried, never untrusted, with a table of handshake error
shapes; the whole-enroll cap and a cancel during a wait; and ten tampered responses
against a fake hub, each saving nothing. The loop runs against `connecthub_test.go`, a
fake hub with Connect over mTLS and the hub's epoch, seq and
duplicate-identity rules: hello and snapshot contents, the four ends and
everything else retried, the same failure tried again and again (a closed
port, an impostor, an address that never answers, `UNAVAILABLE`, a reset
after `HelloAck`), each backing off with its status and metrics, the
backoff's growth, cap and jitter (the first wait included) and its reset
after a connect, after a wake in backoff and after a wake in the gap
between tries (a real 1 s gap, the wake checked to land inside it, read
from `connect.backoff`; the
`pacer` is also tested on its own), an unchanged routing burst that
leaves it alone, the hub's addresses (`HelloAck` and `EnrollResponse`
alike) filtered then capped at 16, each HelloAck check logged with its
`check` and `value`, a hub
restart on the same port, a snapshot cut short
never sent on the next stream, its seq taken before it is built, results
resent until acked (unknown and pruned ids, a `SnapshotRequest`), the
command log pruned by hub time with an agent clock 2 h fast, hub time as
`hub_time` plus the monotonic time elapsed (the monotonic clock is
replaced in the test so the wall clock can be stepped apart from it) and
an anchor that keeps its monotonic reading, a HelloAck with no
`hub_time` logged as `hub_time_ok=false`, seqs that
never go back (a wiped agent and a restored hub), a displaced stream
backing off, wake and network change while connected and in backoff, a
silent hub caught by the keepalive through a proxy that goes quiet
(`FLEET_SLOW_TESTS=1` runs the 30 s default in real time), a stop not held
up by a hub that stopped reading or by a dial, the status, an oversize
snapshot, and the metrics. Heartbeats come at the hub's 1 s interval one
interval after the snapshot and its result, none while the snapshot is
held, with rising uptime, the current `event_seq` and counters, and the
hook unable to change them; 0, under 1 s, over 15 s, negative or no
interval is 15 s (`FLEET_SLOW_TESTS=1` checks 15 s in real time); a
`SnapshotRequest` waits while a heartbeat's hook is held and its snapshot
goes after it (a heartbeat sent from a goroutine of its own fails this);
and with the hub's reads paused under three 600 KiB snapshots, every
snapshot still arrives whole and directly followed by its results. A
HelloAck epoch or seq too large for `agent.db` saves nothing and is tried
again, and another fleet's HelloAck saves no epoch or floor. The three recorded ends each leave a whole
`ended.json` (0600, this machine, the hub, the time) and an expired
certificate none; `CheckEnded` refuses it with the fix, removes another
machine's, and refuses a damaged or loose one; a write that fails still
ends the loop; enroll removes it; and a child process killed with SIGKILL
at random moments while it rewrites the file leaves none or a whole one.
Discovery runs against a fake `Discover` (no real multicast): an answer
tried as it arrives (the browse would run a minute), ten stray answers
dropped without a dial and an impostor with the right name that gets a
handshake and nothing else, no browse while the cached address works, a
browse after the cached address failed that finds nothing falling through
to the configured address, a failed browse, one browse per attempt and a
wake starting the next at once, answers that accept and never reply cut at
their budget and by the phase cap, the drop reasons, the cap of 16, which
addresses are dialed (a table: loopback, unspecified, multicast, broadcast,
IPv4 inside IPv6, link-local IPv6 with and without its zone), and
`fleet_agent_discovery_seconds` counting a miss and not a stop; an injected
querier that rewrites an answer after handing it over, read only after its
query returned (under `-race`), a drained failed query and the query
parameters; enroll with no hub finding the hub past a stray and an impostor
with one request, past three silent answers within their budgets, and
exiting with nothing sent and the right message for each case (nothing
answers, a stray, an impostor, a closed port, a silent answer, a mix, the
browse failing, the time cap). `hub-addresses`: a HelloAck list saved and
used after a restart while the old address refuses; a changed list
replacing the saved one; an empty, invalid or unchanged list not rewritten;
a damaged, loose or folder file falling back to the identity list; a failed
save keeping the stream and the old list, then saved on the next HelloAck;
repeats dropped; enroll removing it; and a child killed with SIGKILL while
it rewrites the file leaving none or a whole list (a child that ended
before the kill fails the test). `FLEET_MDNS_LIVE=1` browses real
multicast for a fake fleetd advertised on this machine, on loopback and on
the default interface; `FLEET_MDNS_WIFI_HUB=<instance>` is the
50-browse Wi-Fi measurement against a real hub, tracked in #154, row 5 (it
times the hand-over, up to one query after the reply arrived). The `fleet-agent` binary's
tests are built with `-tags fleet_nomdns` (a finder that finds nothing) and
never send a query.

Renewal runs end to end against `conntest`'s real `RenewCert` handler
(its Connect served by the fake): with a fake agent clock nothing is sent
a minute before day 60 of the 90-day life, and a minute after it the next
check renews over the stream's own connection while the stream stays up,
`identity.json` stays byte for byte, the next handshake presents the new
serial and the metrics follow it; the renewed pair is renewed again at
day 60 of its own life over a connection that presents it, replacing
`cert.pem`; a renewal stopped before the call, after the answer and after
the rename ends whole on the next start with one stored renewal and one
new serial; a lost answer gets the stored certificate. Against the fake:
sixteen tampered answers (the URI SAN, the CA's end and the 64 KiB cap
among them) and an oversize one each save nothing and keep `renew.key`
and the stream; `UNIMPLEMENTED`, `UNAVAILABLE`, `INTERNAL` and a refusal
are retried with the same key and never end the loop; one renewal at a
time; a stop mid-call and a stream dropped mid-call each logged as
`renew.cancelled` before Run or the session moves on, and no `RenewCert`
from a connection without a stream; an unwritable `cert.pem` retried; a
valid or broken `cert.pem` replaced by a renewal; a damaged or loose
`renew.key` reported with its path and fix, kept, and renewal going on
once it is removed; an agent clock an hour behind the hub; a renewed pair
that starts two hours in the future not presented until it starts and
starting no renewal; nine kinds of invalid `cert.pem` falling back,
logged and untouched; the pair that ends later used; a leftover
`renew.key` removed at start and another kept; the expiry check and gauge
on the pair that ends last by hub time, and a failing renewal ending with
`EndCertExpired` at the next attempt; an agent clock 75 days fast
renewing nothing while the hub's certificate is young and only once
against a hub that signs on another clock; an agent clock 40 days fast
not ending the loop; a renewal waiting one interval after the last
across a reconnect; a lead at the CA's end skipped once; a live renewal
of a future pair keeping the enrolled one; a refused pair swapped for
the other at the next attempt and back after Connected; an answer ending
before the renewed lead refused; a `renew.key` that cannot be removed
reported (macOS); hub time counting 61 and 91 days asleep on a fake sleep-counting clock; a `due_at_once` mark clearing once a correct `HelloAck` shows the pair young; a `HelloAck` with no `hub_time` falling back to the agent's clock; a stream that connected and dropped keeping its pair, and an attempt with no handshake not switching; the switch only to a pair inside its life; the pair choice in a 17-case table; the due time at day 60 to the
second; enroll removing `cert.pem` and `renew.key`; and a link presenting
a swapped pair at its next handshake.
