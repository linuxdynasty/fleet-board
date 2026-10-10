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
  registry held it (a row a crash left live is displaced silently).
  `cancel` ends a machine's stream with a chosen Bye (rows 5b and 13a);
  `byeAll` runs on `http.Server.Shutdown` (`RegisterOnShutdown`): every
  stream gets `Bye{HUB_RESTART}` and its handler returns, so `fleetd run`'s
  stop does not wait out its 10 s grace, and a Hello that commits after it is
  answered `Bye{HUB_RESTART}` with its row closed.
- **Per stream** (`stream.go`): the handler goroutine runs the ingest loop
  and the teardown; one reader (the only `Receive` caller) keeps Heartbeats
  in memory only (the registry's last-heartbeat time, read for
  `HelloIn.Live`; no store write), ends the stream on the agent's `Bye` or a
  second `Hello`, and hands every other message in order to a bounded queue
  (64); one writer (the only `Send` caller) sends queued messages (16) and
  then, when the stream ends, its Bye within 2 s. The teardown wakes the
  reader with the response's read deadline, cuts a send still blocked by
  flow control after 2 s with the write deadline, handles what was queued,
  removes the stream from the registry, then closes its row with
  `statedb.EndConnection` (not for a displaced one) on a context that the
  stream's end does not cancel. No goroutine touches the stream after the
  handler returns, and no transaction spans a send.
- **Handler table** (`arms.go`): one handler per `ConnectRequest` arm, added
  by later rows from files of their own (`arms.add` in an `init`). An arm
  with none is counted in `fleet_hub_connect_unhandled_total{kind}`, logged
  once per kind per stream (`hub.connect.unhandled`) and ignored.
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
| `fleet_hub_connect_refused_total{cause}`, `connect.refused` (`cause`, `reason`, `peer`, `machine_id`) | a refused Hello (warn) |
| `connect.failed` (`step`: `validate`, `store`; `err_kind`, `machine_id`) | the hub's failure at a Hello or a stream's close (error) |
| `fleet_hub_connect_unhandled_total{kind}`, `hub.connect.unhandled` (`machine_id`, `epoch`, `kind`) | a message of an arm with no handler yet |
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
byeAll, the queue bound, the limiter and the handler table. They dial the real
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
