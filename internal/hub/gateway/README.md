# internal/hub/gateway

The agent listener (`listen.agent`, [02j](../../../docs/specs/02j-hub-runtime.md#listeners)):
TLS 1.3 with the CA-issued server certificate, serving `AgentService`.

```go
certs, err := gateway.NewCerts(gateway.CertsOptions{Store: st, Identity: id, Names: names, Telemetry: tel})
err = certs.Ensure(ctx)     // at start: re-issue if the names changed or it is past two thirds
go certs.Run(ctx, ticker.C) // hourly in fleetd run; stops with ctx
srv, err := gateway.NewServer(gateway.ServerOptions{
	Certs: certs, Store: st, Identity: id, Revoked: revokedList, Telemetry: tel,
}) // srv.ServeTLS(ln, "", "")
```

- **TLS 1.3 only**, with the certificate from an atomic pointer, so a
  re-issue takes effect on the next handshake without a restart. The chain
  sent is the server certificate, then the CA: an enrolling agent holds only
  the CA's SHA-256 from its token and finds the root in the chain by it (02g
  step 2).
- **Client certificates** are optional (an enrolling agent has none) and,
  when sent, must chain to the hub CA with client-auth usage
  (`VerifyClientCertIfGiven`; the hub's own server certificate therefore fails).
  `tls.Config.VerifyConnection` refuses a certificate whose serial
  (`ca.SerialHex`) is in the `RevokedSet` (the `revocation.List` in fleetd);
  it runs on resumed TLS 1.3 sessions too, so a session ticket from before a
  revoke does not get back in. `RenewCert` checks the machine and serial in
  the store on every call; the other RPCs do with row 5. A middleware puts
  the verified client certificate (the first verified chain's leaf) in the
  request context; handlers read it with `peerCert`, never from the request.
- **Enroll** ([02g](../../../docs/specs/02g-agent-security.md#enrollment-trust-anchor-and-retry-a1)),
  checked in this order, nothing burning the token before the writer job:
  1. `install_id`: 1 to 64 printable ASCII characters without spaces (over 64
     bytes `FIELD_TOO_LARGE`, else `INVALID_ARGUMENT`).
  2. The token: `ca.ParseToken`, and its CA hash must be this hub's CA;
     otherwise `ENROLL_TOKEN_UNKNOWN`.
  3. The proto range (`protoversion.Negotiate`): `ENROLL_PROTO_UNSUPPORTED`.
  4. The CSR (`ca.ParseCSR`): `ENROLL_CSR_INVALID`.
  5. `statedb.CheckEnroll` on the read pool: unknown, a replay of the stored
     response, used, expired, or a token naming a previous machine.
  6. A fresh token: a ULID machine id, `ca.IssueAgent`, then `statedb.Enroll`,
     the one writer job that burns the token and inserts the machine, its
     serial and the response (or answers the winner's replay or a token error
     when another Enroll got there first).

  The response is rebuilt from `hub_identity` (CA, fingerprint, salt,
  `fleet_id`, `hub_addresses`) and the stored certificate, for a first answer
  and a replay alike. A valid client certificate on the connection is
  ignored, but an expired or revoked one fails the handshake before Enroll
  runs, so the agent calls Enroll without one. A
  store or writer error is `UNAVAILABLE` with `retryable` set; a cancelled
  write has an unknown outcome, and the agent's retry finds the replay. A token
  naming a previous machine is the hub's failure (`INTERNAL`, never burned;
  re-enroll is plan row 13a).

  **Audit.** A first Enroll's `machine.enroll` row is written by
  `statedb.Enroll` in its job; a replay writes none. Every refusal (the
  caller's error) also writes `machine.enroll_failed` (target `machines`,
  `outcome` `FAILED`, the reason, and a detail with the gRPC `code` and the
  `peer` without its port) as a writer job of its own, waited for at most 5
  s, within the caps of `internal/hub/auditcap`: one row per (reason, peer)
  per minute, 10 per minute in all, then one summary row per minute
  (`window_start`, `suppressed`, `distinct_peers`, `reasons`; `occurred_time`
  the minute's start) written before the next refusal's row or, for the
  minute in progress, by `Server.FlushAudit`, which `fleetd run` calls after
  the listener's `Shutdown` and before the store closes (a crash loses it:
  best effort). The cap is in memory and a restart empties it. The hub's own failures write no
  row.
- **RenewCert** ([02g](../../../docs/specs/02g-agent-security.md#certificate-renewal-a4)),
  checked in this order:
  1. A verified client certificate, else `CREDENTIALS_INVALID`. Its CN is the
     machine id and its serial the peer serial; nothing comes from the request.
  2. The CSR (`ca.ParseCSR`, the Enroll rule), then a key other than the
     presented certificate's: `RENEW_CSR_INVALID` for either.
  3. `statedb.Renew`: no machine row or an unknown or revoked serial is
     `CREDENTIALS_INVALID`, a `REVOKED` machine `MACHINE_REVOKED`; the same
     machine and CSR key return the stored certificate (`result="replayed"`);
     otherwise `ca.IssueAgent` (90 days, never past the CA) runs inside the
     writer job that inserts the serial and the `cert_renewals` row, stamps
     the machine and appends `machine.cert_renewed`; a replay writes no row.

  The old serial is not revoked. A store error is `UNAVAILABLE` with
  `retryable`; the retry with the same CSR gets the stored certificate.
- **Connect** ([02g](../../../docs/specs/02g-agent-security.md) "Hello refusals",
  02i, [03n](../../../docs/specs/03n-reconnect.md); plan row 5 PR 5a-ii). The
  machine id is the verified certificate's CN, which its one URI SAN
  `fleet://machine/<id>` must name; the serial is the certificate's. Checked in
  order: the certificate (before anything is read), the first message within
  `HelloWait` (30 s, `ServerOptions.HelloWait` in tests) and that it is
  `Hello`, `machine_id`, `boot_id` and `install_id` (the Enroll install_id
  rule), the proto range (`Bye{PROTO_UNSUPPORTED}`), the Hello rate limit (one
  per machine per `HelloEvery`, 5 s, marked when it passes; in memory, so a
  restart clears it), the `RevokedSet` (`Bye{REVOKED}`: after a restore the
  file may hold serials state.db lacks), then `statedb.Hello` as one writer
  job (`Bye{REVOKED}`, `Bye{UNKNOWN_MACHINE}`, or the error table). Every
  answer is in one table, `connectAnswers` (`connect_refuse.go`); a refused
  Hello writes nothing. After the commit the stream is registered and
  `HelloAck` queued: fleet_id, the negotiated proto version, the epoch,
  hub_time, `HeartbeatInterval` (15 s), `MaxMessageBytes`, hub_addresses,
  settings_epoch, settings_generation, hub_last_event_seq and a
  hub_instance_id minted per process start; no hub_capabilities (snapshots
  arrive whole).
- **Registry** (`registry.go`): one live stream per machine, registered only
  after the commit; the higher epoch wins whatever order two results arrive
  in, so a late lower one gets `Bye{DUPLICATE_IDENTITY}` itself; removal is by
  stream, so a displaced stream's close never removes the winner. A displaced
  stream gets `Bye{DUPLICATE_IDENTITY}`, counted and logged only when the
  registry held it (a row a crash left live is displaced silently); a late
  lower epoch that lost is counted and logged too, gets the same Bye, and
  logs no `reconnect.closed` (it was never registered and its row is
  already closed).
  `cancel` ends a machine's stream with a chosen Bye (rows 5b and 13a);
  On `http.Server.Shutdown` (`RegisterOnShutdown`), `beginShutdown` ends a
  context every pre-Hello wait watches, then `byeAll` ends every live
  stream: a stream waiting for its first message or for its Hello job, and
  every live one, gets `Bye{HUB_RESTART}` and its handler returns, so
  `fleetd run`'s stop does not wait out its 10 s grace. The shutdown
  cancels only the wait: a Hello job already queued still commits (the
  store's Close finishes it), so a `machine_connections` row can stay open
  after a shutdown with no stream behind it, as after a crash. The
  machine's next Hello closes it as displaced (heartbeat age unknown) and
  counts no duplicate; plan row 5b's hub-start sweep must expect such rows.
  The same holds for an agent that leaves while its Hello waits (logged as
  a departure, `reconnect.closed{err_kind=reset}`, at info). A Hello that
  commits after the hook ran is answered `Bye{HUB_RESTART}` with its row
  closed. `http.Server.Shutdown` waits for connections, not handlers: an
  agent that drops the connection on its Bye lets it return while that
  stream's handler is still closing its row. So `Server.Shutdown`
  (`inflight.go`) then waits, bounded by its context, for every handler the
  listener runs (a count kept around the whole mux); when the context ends
  first it logs `hub.shutdown_wait_timeout{handlers}` and returns the error.
  The store may close only after it returns.
- **Per stream** (`stream.go`): the handler goroutine runs the ingest loop
  and the teardown; one reader (the only `Receive` caller) keeps Heartbeats
  in memory (the stream's last-heartbeat time, read for `HelloIn.Live` and
  for silence; a clock skew sample) and wakes the handler goroutine, ends
  the stream on the agent's `Bye` or a second `Hello`, and hands every other
  message in order to a bounded queue (64), marking the stream stalled
  while the queue is full; one writer (the only `Send` caller) sends queued messages (16) and
  then, when the stream ends with a Bye, what is still queued, in order,
  and then the Bye, all within 2 s. The teardown wakes the
  reader with the response's read deadline, cuts a send still blocked by
  flow control after 2 s with the write deadline, handles what was queued,
  removes the stream from the registry, then closes its row with
  `statedb.EndConnection` (not for a displaced one) on a context that the
  stream's end does not cancel; that job takes an `ONLINE` or `STALE`
  machine `OFFLINE` when the row was the live one (`stream_closed`,
  `agent_bye`, `ingest_stalled`, `offline`; never for a displaced stream,
  #138, nor for `hub_restart`). No goroutine touches the stream after the
  handler returns, and no transaction spans a send.
- **Machine state** (`presence.go`, plan row 5b, 03n): `Server.Housekeep`
  is the housekeeping job (`fleetd run`, every 5 s). It reads every machine
  and its live row (`statedb.MachineStates`) and, for an `ONLINE` or
  `STALE` one, takes the silence since the newest of this process's start,
  the live row's `Hello`, the live stream's last applied Snapshot (the
  row 5 ruling starts the `STALE` clock there), its last Heartbeat and the
  machine's `last_seen_time` read with its state (hub clock):
  `OfflineAfter` (3 min) is `OFFLINE` (`statedb.MarkState` also closes the
  live row as `offline`) and the stream is cut; `StaleAfter` (45 s) while
  `ONLINE` is `STALE`; while `STALE`, a stream stalled for `StaleAfter` or
  more is cut (`ingest_stalled`). A cut sends no Bye: it ends the stream
  (the handler returns nil, so the stream ends with an OK status) and
  cancels the stream's context (arm handlers get it), so a handler waiting
  on the store returns, and messages drained after the cut see it
  cancelled. A stream that already ended on its own is not cut or counted. On a Heartbeat the handler goroutine runs one
  `statedb.HeartbeatSeen` job when `last_seen` is due (`LastSeenEvery`, 1
  min, the Heartbeat's receive time) or when the machine went `STALE` while
  this stream was live (then back to `ONLINE`); a `STALE` from the start
  sweep waits for the Snapshot, whose commit brings any non-`REVOKED`
  machine back to `ONLINE` (`machine.state_changed{reason="snapshot"}`).
  The Snapshot arm records its hub time on the stream (`snapped`, not
  `beat`, which feeds the displaced stream's heartbeat age) after the
  commit. Neither that record nor the pass's read can lose the Snapshot:
  the Snapshot tx moves `last_seen_time`, a pass that reads after the
  commit counts silence from it, and a mark judged from a read before the
  commit is refused in its job (`Mark.Seen`, 03f race table).
  `fleet_hub_machine_state` is
  set at each change and each pass, and for the start sweep's changes by
  `Server.RecordStates` (fleetd run, once the listener exists); the clock skew is the median of the
  last five `sent_time` samples (`Hello` and Heartbeat), in memory.
- **A client cancel alone is not seen.** A test client that only cancels
  its context leaves the hub's reader waiting: the Go HTTP/2 client with
  connect-go sends nothing once the response has started while the request
  body is open. A client must `CloseRequest` or close the transport, as the
  real agent does (`internal/agent/conn` `session.go`); a peer that goes
  silent is caught by `STALE` at 45 s and `OFFLINE` (and the cut) at 3 min.
- **Handler table** (`arms.go`): one handler per `ConnectRequest` arm, added
  by later rows from files of their own (`arms.add` in an `init`). An arm
  with none is counted in `fleet_hub_connect_unhandled_total{kind}`, logged
  once per kind per stream (`hub.connect.unhandled`) and ignored.
- **Snapshot** (`snapshot.go`, plan row 5 PR 5c1; 03n, 02i, row 5 ruling
  forks 1 and 2a): on the stream's handler goroutine the stream must still be
  its machine's live one and the Snapshot whole (no `snapshot.parts`);
  `ingest.Clean` cuts and scrubs its text; then the Snapshot is
  one writer job (`ingest.SubmitSnapshot`, then `Pending.Wait`; the statedb core with the current-epoch check and agent
  rollback, `ServerOptions.Appliers` in order, then `outbox.Expire` for
  the machine's due commands, then ONLINE), compared with
  the values the stream's `Hello` carried. Only its commit marks the
  machine ONLINE; the Hello transaction never does. As for a Hello, the
  wait for the job ends when the agent leaves, a housekeeping cut or
  Shutdown begins (`snapshot.abandoned` at info, `result="abandoned"`: the
  outcome is unknown and the job may still commit), so the teardown closes
  the row before the store closes. Once the row's close is queued, the
  teardown reads every abandoned job's result in queue order (the one
  writer runs them first; its own `endWait` bound) and logs their state
  changes before the close's, so every committed change is logged in
  commit order. The abandoned reason never waits for the stream's end
  (the reader may be blocked on the full ingest queue that only this
  goroutine drains); displacement alone never abandons a wait. After the commit:
  `outbox.Closed` logs and counts the commands the job expired (an
  abandoned job's, at the teardown, when its result is read in time),
  the outbox's `ready(machineID, epoch)` (pending commands go out from
  it), then `ServerOptions.OnSnapshotApplied(machineID, epoch)` when set
  (each must check the stream is still live, since it also runs for a
  Snapshot handled after the agent left; `ready` does), and
  `SnapshotRequest{seq_rollback}` when the epoch's first Snapshot had a seq
  not above the stored event seq (counted and logged when the send queue
  is full). A Snapshot not applied
  ends the stream (no Bye) so the agent reconnects with a fresh one, except
  `not_current`: a newer Hello already displaced the stream.
- **Events** (`arm_events.go`, `events_counters.go`, `events_prune.go`;
  plan row 5 PR 5c2; 02i, 03n, row 5 ruling forks 2a and 6): on the
  handler goroutine an Event needs the machine's live stream with its
  Snapshot applied and a seq above the last one taken on the stream
  (holes are normal); otherwise it is dropped (`event.refused`, once per
  reason per stream) and an order break (`before_snapshot`, `seq_order`)
  sends `SnapshotRequest{out_of_order}` under the per-machine 5-minute limit
  it shares with `event_lost`; the stream stays open. `ingest.CleanEvent`
  scrubs its free text; then `ingest.SubmitEvent` queues ONE writer job
  without waiting (at most `maxInflight`, 256, in flight per stream; at
  the cap the arm waits for the oldest; so one stream holds at most 256
  queued jobs in memory, each with the decoded Event and its wire bytes:
  about 512 MiB at the 1 MiB message cap in the worst case, about 150 KiB
  at the 300-byte replay size), and the serve loop settles the
  outcomes in order as they come: counted in
  `fleet_hub_events_total{kind, result}`; a failure (`event.failed`) ends
  the stream so the agent sends again what was not committed (the store's
  chain check fails every later job). The teardown settles what is still
  in flight within `endWait` (`event.unsettled` past it). The replay phase
  (first replayed event to the first live one) is the span
  `hub.replay.ingest` and `replay.done`; a ring-overflow gap the Snapshot
  found is logged as `replay.gap` at the first replayed event.
  Heartbeat counters are judged after the Snapshot only: a rise of
  `evicted_total` is `event.lost`, a gap marker and `evicted_total` stored
  (`statedb.RecordEventLoss`), `fleet_hub_event_gap_total`, and
  `SnapshotRequest{event_lost}` at most once per `EventLostEvery` (5 min)
  per machine, the limit `out_of_order` shares (in memory); `fleet_hub_replay_backlog_events` is
  `Heartbeat.event_seq` minus the committed seq. EventAck (row 7 ruling,
  fork 1; round 3, 3e): a committed seq, at most one per `AckEvery` per
  stream, the held-back last one on a timer, queued for the stream's writer
  as `ConnectResponse.event_ack` (`streamAcks`; a full queue skips it,
  `fleet_hub_event_acks_total{machine, result="queue_full"}`, or
  `result="ended"` once the stream ended); HelloAck lists
  `event.ack` 1. A failed job ends the stream before any ack covers it.
  `Server.PruneEvents` is the hourly housekeeping job that deletes history
  older than `EventRetention` (7 days) in jobs of 5,000 rows.
- **Commands** (`outbox.go`, `arm_commands.go`; plan row 5, PR 5f): the
  server builds the `internal/hub/outbox` Service on its store and runs its
  sender until `Shutdown` or `Close` (`FlushAudit` waits for it); callers
  reach it through `Server.Outbox()`. The registry is its `Streams`: a
  command goes only to the machine's live stream of the command's epoch,
  without blocking (a full queue is retried by the sender). `ready(machine,
  epoch)` is the seam the Snapshot arm calls after its transaction
  commits (`NewServer` wires it ahead of `OnSnapshotApplied`); nothing is sent to a machine before it, and it refuses an epoch
  that is not the live stream's. A stream's close tells the outbox the
  epoch is gone. The arms `command_ack`, `command_progress` and
  `command_result` hand the message to the outbox with the stream's machine,
  epoch and whether it is still live, on a write context the stream's end
  does not cancel (5 s); after a stored or already-stored result the
  handler queues `ResultAck`, waiting for room in the stream's queue.
- **Request cap**: `connect.WithReadMaxBytes(MaxMessageBytes)`, the 1 MiB of 02e;
  on Connect the stream ends `RESOURCE_EXHAUSTED` and its row closes as
  `stream_closed`.
- **AgentService**: Terminal and SessionLog answer `Unimplemented` until their rows.
  It speaks Connect, gRPC and gRPC-Web over HTTP/1.1 and HTTP/2.
- **Timeouts**: `ReadHeaderTimeout` (10 s) and `IdleTimeout` (2 min, a
  connection with no request or open stream). No `ReadTimeout`: the agent
  streams are long-lived.
- **Re-issue** (`Ensure`): reason `names_changed` when the SANs differ from
  the wanted names, `renewal` at two thirds of the lifetime. The new
  certificate is signed by the stored CA and written with
  `statedb.ReplaceServerCert` (one writer transaction) before it is served; on
  a failure the old one stays in service and the next tick retries.
- `Serves(name)` reports whether the certificate served now names `name`;
  the mDNS advertiser (`internal/hub/discovery`) asks it for the SRV target,
  so after a failed re-issue it refuses to advertise a name agents cannot
  verify. The wanted names (`CertsOptions.Names`) are `ca.AgentNames`: the SRV
  target is in them even when `tls.agent_names` is set, so adding it to an
  older certificate is one `names_changed` re-issue.

## Signals

| Signal | When |
|---|---|
| `fleet_hub_enroll_total{result}` | every Enroll: `enrolled`, `replayed`, `refused` (the caller's error), `failed` (the hub's) |
| `enroll.completed` (`machine_id`, `replayed`, `hostname_mismatch`) | a new or replayed enroll |
| `enroll.refused` (`reason`, `peer`) | a refusal; `reason` is the ErrorDetail reason without `REASON_CODE_` |
| `enroll.failed` (`step`, `err_kind`) | the hub's failure; `step` is `store`, `previous_machine`, `random`, `sign` or `encode` |
| `fleet_hub_enroll_audit_rows_total{result}` | a refusal's audit outcome: `written`, `suppressed` (capped), `summary` (a minute's summary row), `write_failed` |
| `enroll.audit_failed` (`rows`, `err_kind`) | audit rows for a refusal the store did not take (the refusal is still answered) |
| `fleet_hub_agent_cert_renewals_total{result}` | every RenewCert: `renewed`, `replayed`, `refused`, `failed` |
| `cert.renewed` (`machine_id`, `result`, `serial`, `not_after`) | a new or replayed renewal |
| `cert.renew_refused` (`result`; refused: `reason`, `cause`, `peer`, `machine_id`; failed: `step`, `err_kind`, `machine_id`) | a refusal (warn) or the hub's failure (error); `cause` is `no_client_cert`, `csr_invalid`, `csr_same_key`, `machine_unknown`, `machine_revoked` or `serial_unknown`; `step` is `store`, `sign` or `encode` |
| `fleet_hub_auth_failures_total{reason="revoked"}`, `tls.client_cert_revoked` (`serial`, `machine_id`) | a handshake refused for a revoked serial |
| `fleet_hub_reconnects_total{machine, reason}`, `fleet_hub_epoch{machine}`, `reconnect.hello` (`machine_id`, `epoch`, `last_epoch`, `last_event_seq`, `hub_last_event_seq`, `reason`, `peer`), span `hub.connect.hello` | an accepted Hello |
| `fleet_hub_duplicate_identity_total{machine}`, `identity.duplicate` (`machine_id`, `old_epoch`, `new_epoch`, `old_boot_id`, `new_boot_id`, `old_peer`, `new_peer`, `heartbeat_age_ms`) | a live stream displaced by a newer Hello |
| `reconnect.closed` (`machine_id`, `epoch`, `reason`, `row_closed`) | a stream ended |
| `machine.state_changed` (`machine_id`, `from`, `to`, `reason`), `fleet_hub_machine_state{machine, state}` | a committed state change: `silence`, `heartbeat`, `snapshot` (the Snapshot tx's `ONLINE`), or the live row's end reason |
| `hub.stream_closed` (`machine_id`, `epoch`, `reason`, `stalled_s`, `queue_depth`), `fleet_hub_stream_closed_total{reason}` | housekeeping cut a stream (warn): `offline` or `ingest_stalled` |
| `fleet_hub_clock_skew_seconds{machine}` | the median skew after each `sent_time` sample |
| `fleet_hub_connect_refused_total{cause}`, `connect.refused` (`cause`, `reason`, `peer`, `machine_id`) | a refused Hello (warn) |
| `connect.failed` (`step`: `validate`, `store`, `wiring`, `heartbeat`; `err_kind`, `machine_id`) | the hub's failure at a Hello, a stream's close or a Heartbeat's store job (error) |
| `fleet_hub_connect_closed_before_hello_total{err_kind}`, `reconnect.closed` (`epoch` 0, `reason` `before_hello`, `err_kind`: `oversize`, `reset`, `timeout`, `shutdown`) | a stream with a certificate that ended before a Hello was accepted |
| `fleet_hub_connect_unhandled_total{kind}`, `hub.connect.unhandled` (`machine_id`, `epoch`, `kind`) | a message of an arm with no handler yet |
| `snapshot.applied` (`machine_id`, `epoch`, `seq`, `bytes`, `ms`, `event_generation`, `text_cut`, `text_scrubbed`, `expired`), `fleet_hub_snapshot_apply_seconds{result="applied"}`, `fleet_hub_snapshot_bytes`, span `hub.snapshot.apply` | a Snapshot committed; the machine is ONLINE |
| `snapshot.refused` (`reason`: `not_current`, `parts`, `revoked`, `unknown_machine`, `invalid`), `snapshot.failed` (`step`: `store`, `applier`; `err_kind`), `result="refused"` / `"failed"` | a Snapshot not applied (warn / error) |
| `snapshot.abandoned` (`reason`: `agent_left`, `cut`, `shutdown`), `result="abandoned"` | the stream ended while the Snapshot's job waited (info; outcome unknown) |
| `snapshot.rollback` (`machine_id`, `epoch`, `causes`, `event_generation`), `fleet_hub_agent_rollback_total{machine}` | an agent rollback (02i rule 3), with a `machine.install_changed` audit row |
| `snapshot.request_dropped` (`machine_id`, `epoch`, `reason`), `fleet_hub_snapshot_requests_dropped_total{machine}` | a `SnapshotRequest{seq_rollback}` the full send queue refused |
| `outbox.result_ack_unsent` (`command_id`, `machine_id`, `epoch`) | a stored result whose `ResultAck` the stream could no longer take; the outbox's own signals are in its README |
| `fleet_hub_server_cert_issues_total{reason, result}`, `tls.server_cert_issued` (`reason`, `names` count, `not_after`), `tls.server_cert_issue_failed` (`reason`, `err_kind`, `not_after` still served) | server certificate re-issues |

No token, secret or part of its hash, no facts, no key, no certificate and no
SAN list is logged. A failed handshake is also a debug-level
`http.server_error` (`listener="agent"`, `kind="tls_handshake"`), and a
recovered handler panic an error-level one (`kind="panic"`), through
`telemetry.HTTPErrorLog`, never plain text on stderr.

Tests: `CGO_ENABLED=0 go test ./internal/hub/gateway/`. Connect's tests open
real streams with an enrolled machine's certificate: HelloAck's fields and the
stored row, the epoch rule, displacement (Bye, heartbeat age, one writer job
between the two HelloAcks, so Heartbeats write nothing), a crash-left row
counted as no duplicate, the agent's Bye, an unhandled arm, Shutdown with two
live streams, every refusal of the 02g table (nothing stored, last_epoch and
last_seen unchanged, counted and logged), the Hello wait, a second Hello, an
unknown reason, the rate limit's boundaries, the 1 MiB cap and a closed
store; `registry_internal_test.go` covers the out-of-order register, cancel,
byeAll, the queue bound, the limiter and its sweep, and the handler table;
`state_test.go` and `state_stall_test.go` drive `Housekeep` with the fake
clock: `STALE` at 45 s and `OFFLINE` at 3 min (the stream cut, the row
`offline`), a Heartbeat bringing a `STALE` machine back in one job,
`last_seen` once a minute and no job for other Heartbeats, a displaced
stream's close leaving the machine `ONLINE` (#138, also when its own close
wins the race with the registry), the skew median, and a
stalled reader cut only at `STALE` after 45 s (never while `ONLINE`).
`state_return_test.go` checks that a `STALE` from the start sweep and a
`STALE` whose stream was displaced are not brought back by Heartbeats on
the new stream, and that silence counts from the live row's `Hello`, not
the hub's start; `snapshot_state_test.go` that the Snapshot brings a
`STALE` machine back with one `machine.state_changed{reason="snapshot"}`
and that silence then counts from the Snapshot; `registry_internal_test.go` that a cut finds only a
running stream of its epoch.
`writer_internal_test.go` drives the writer with a stub (queued messages,
then the Bye, under one deadline; the cut after the wait) and a request
with no response controller. `connect_end_test.go` covers Shutdown with a
stream that never said Hello and with a Hello stuck behind the writer, a
Hello committed during Shutdown (an `export_test.go` hook), messages queued
before the agent's Bye (one test with a slow arm handler), the limit's
place after the earlier checks, the registry after an agent's Bye, and a
first message that is too big or never comes. The Snapshot tests
(`snapshot_test.go`, `snapshot_epoch_test.go`) prove the machine stays
ENROLLED after HelloAck and is ONLINE with its facts and policy only after
the Snapshot commits (text cut and scrubbed, the hook, the log and the
histogram), a failing applier rolling everything back and ending the
stream, a Snapshot in parts refused, a Snapshot of a displaced epoch
refused in its transaction (an `export_test.go` hook completes the newer
Hello first), a seeded seq rollback answered with one
`SnapshotRequest{seq_rollback}` (a second low-seq Snapshot starts no
generation and asks for nothing) whose answer replaces the facts, a
Snapshot whose agent left logged as abandoned (not failed) while its job
still commits and its ONLINE is logged before the close's OFFLINE, a
Snapshot cut by housekeeping logged as abandoned with reason `cut`, and a
panicking applier rolled back with the hub still serving;
`snapshot_internal_test.go` counts a dropped `SnapshotRequest`. They dial the real
listener with `crypto/tls` and the generated gRPC client against a real
`state.db` and a real `revocation.List`: enroll (certificate CN and URI SAN,
chain, accepted as a client certificate), replay until 24 h past the TTL,
every refusal of the 02g table (each with one `machine.enroll_failed` row
without the token), the per-peer cap and the next minute's summary with a fake
clock, the summary written at shutdown, a refusal that must not burn the token,
store failure, a token naming a previous machine, concurrent first enrolls
(one machine row), a revoked serial refused fresh and on a resumed session,
the server certificate refused as a client certificate, TLS 1.2 refused,
server certificate renewal with a fake clock and a tick channel, and
RenewCert: a new serial for the CSR key, the stored certificate on a repeat,
each refusal of the 02g table, store failure, concurrent identical calls
(one serial), and a CA with less than 90 days left. The listener tests read one
clock, `wallNow`, for issuing and verifying, so they pass on any date.

`shutdown_wait_test.go` holds a row close (an `export_test.go` hook) under
an agent that dropped its connection: Shutdown waits for it, and ends with
its context.
