# internal/hub/gateway

Read the root `AGENTS.md` first; it applies here. Load `fleet-go`,
`fleet-security` and `fleet-telemetry-coverage`; `fleet-contract-rigor` for
anything that touches `AgentService`. Rules for this package:

- TLS 1.3 minimum, always. Never add a plain-text or TLS 1.2 path.
- The served certificate changes only after the store commits the new one,
  and only through the atomic pointer; never mutate a `tls.Certificate` in
  place.
- A failed re-issue keeps the old certificate in service, logs
  `tls.server_cert_issue_failed` and counts `result="failed"`; it never stops
  the listener.
- Every goroutine has an owner: `Run` stops with its context, and the caller
  waits for it before closing the store.
- Never log a key, a certificate or the SAN list; counts, reasons and
  `not_after` only. Never log an enroll token, its secret, any part of the
  secret's hash, or the machine facts.
- Enroll burns a token only in `statedb.Enroll`'s writer job; every check
  before it (fields, token format and CA hash, proto range, CSR) leaves the
  token unburned. Store errors answer `UNAVAILABLE` with `retryable`.
- The revocation check runs in `VerifyConnection` (fresh and resumed
  handshakes); the `RevokedSet` it calls must never block.
- Connect: identity only from the verified certificate; nothing is sent
  before the Hello transaction commits; every refusal goes through the one
  table in `connect_refuse.go` (02g "Hello refusals" is its spec). Only the
  stream's writer goroutine calls `Send` and only its reader calls
  `Receive`; the handler stops both before it returns. Registry entries are
  removed by stream, never by machine. The reader never writes the store:
  a Heartbeat's store job (last_seen once a minute, STALE back to ONLINE)
  runs on the handler goroutine, only when it changes something.
- Machine state changes go through `statedb` jobs that re-check the state
  and the live epoch in the transaction; a displaced stream's close never
  changes its machine (#138). A housekeeping cut is a transport close with
  no Bye (row 5 ruling, fork 4).
- A new `ConnectRequest` arm gets its own file that registers it with
  `arms.add`; do not grow `arms.go`.
- Tests dial the real listener; a fake clock and a tick channel drive renewal.
- A change here updates this README and `doc.go` in the same commit.
