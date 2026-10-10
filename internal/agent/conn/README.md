# internal/agent/conn

The agent's link to the hub: enrollment
([02g A1](../../../docs/specs/02g-agent-security.md),
[03 W1](../../../docs/specs/03-workflows.md#w1-enrollment-and-first-connect)),
run by `fleet-agent enroll --hub <host[:port]> --token <token>`, and the
connection loop (03 W1 "State machine", 03n W2), run by `fleet-agent run`.

```go
res, err := conn.Enroll(ctx, conn.Options{Dir: dir, Hub: "hub.local", Token: tok, Version: v, Telemetry: tel})
id, err := conn.LoadIdentity(dir)  // ErrNotEnrolled when there is none
addr, err := conn.LoadHubAddress(dir)
removed, err := conn.CheckEnded(dir, id) // *EndedError: the hub ended this machine; do not dial
l, err := conn.NewLoop(ctx, conn.LoopOptions{Dir: dir, Identity: id, DB: db, Version: v, BootID: b,
	Telemetry: tel, Events: wakeAndNet, PushOff: watcher.PushOff, Heartbeat: fillLaterFields})
err = l.Run(ctx)                    // nil after ctx ends; *EndedError when it ended on its own
st := l.Status()                    // state, peer, last failure per address, clock offset, ...
```

`Enroll`, in order, stopping at the first failure:

1. **Nothing leaves before the local checks.** The token must parse
   (`ErrTokenMalformed`), a hub address must be given (`ErrNoHub`: discovery
   comes later in plan row 4), and the state folder must be private.
2. **Never replace an identity.** Until re-enroll exists (later in plan row
   4, and row 13a's `previous_machine`), an enrolled folder is refused
   (`ErrAlreadyEnrolled`, naming the machine and saying to remove only
   `identity.json` and keep `agent.db`) before any network call, and a
   leftover `enroll.key` is removed. The identity file is also written with
   a hard link, which cannot overwrite.
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
   `UNKNOWN`, `INTERNAL`, `DEADLINE_EXCEEDED` or `ABORTED` (a cut stream), or
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
7. The hub address goes to `hub-address`, the identity to `identity.json`
   (temp file, fsync, link, folder fsync), then `enroll.key` is removed.
   `Result.HostnameMismatch` says the hub expected another hostname (the
   rule is `internal/hostname`, shared with the hub); the command prints a
   warning for it.

## Connection loop

`Loop.Run` repeats one attempt until ctx ends:

1. **Certificate first.** Past `CertNotAfter` the loop ends
   (`EndCertExpired`) without dialing.
2. **Candidates.** The cached `hub-address`, then the identity's addresses
   (replaced by `HelloAck.hub_addresses` for the rest of the run), each
   once. One whose handshake fails (refused, timed out after
   `LoopOptions.HandshakeTimeout`, 10 s by default, or untrusted: the same
   pin as enroll) is skipped for the next, logged at `step="dial"`; the
   first that passes is used. The client certificate is the enrolled one.
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
   and `Settings` are logged and ignored until rows 6 and 10. On stop the
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

Not built yet, all later in plan row 4:

- The agent side of certificate renewal (the hub's `RenewCert` handler
  is built, #147), under the ruling that a renewal makes a new key and
  keeps the renewed pair in its own `cert.pem`, never rewriting
  `identity.json`. Until then the loop ends with `EndCertExpired` when
  the enrolled certificate runs out.
- Discovery (mDNS, PR 4), and with it the discovery signals 03 W1 lists:
  the `agent.discover` span (one child per candidate) and
  `fleet_agent_discovery_seconds`. Until then the candidates are the
  cached and configured addresses only, and neither signal is emitted.

**State folder** (04 Q9): `~/.local/state/fleet-agent/` unless
`--state-dir`; created 0700, and a folder or file with any group or other
bit, another owner, or a symlink is refused (`ErrLooseMode`), never re-moded.

| File | Holds |
|---|---|
| `identity.json` | machine id and name, fleet id, CA PEM and fingerprint, client certificate and its expiry, key, account salt and version, the hub's configured addresses |
| `hub-address` | last good `host:port` |
| `enroll.key` | the key, only between a first try and success |
| `ended.json` | the end that stopped the loop (revoked, unknown machine, fleet mismatch), its machine id, hub and time; removed by enroll |
| `agent.db` | `install_id` and the rest ([ring](../ring/README.md)) |

**Signals.** Spans `agent.enroll`, `agent.connect.hello`. Loop logs and
metrics are listed in 03 W1 and 03n W2 "Telemetry" (`connect.*`, `snapshot.*`,
`result.*`; `fleet_agent_connect_attempts_total`, `fleet_agent_connected`,
`fleet_agent_reconnects_total`, `fleet_agent_unacked_results`,
`fleet_agent_cert_expiry_seconds`).
Enroll logs: `enroll.completed{machine_id,
replayed, hostname_mismatch}`, `enroll.refused{reason, peer}`,
`enroll.retry{attempt, err_kind}`, `enroll.hub_untrusted{peer}`,
`enroll.failed{step, err_kind}` with `step`
one of `token`, `hub_address`, `state`, `already_enrolled`, `key`, `call`,
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
