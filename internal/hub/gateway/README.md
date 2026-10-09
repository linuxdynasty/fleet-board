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
  revoke does not get back in. Checking the machine per RPC arrives with row 5.
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
- **Request cap**: `connect.WithReadMaxBytes(MaxMessageBytes)`, the 1 MiB of 02e.
- **AgentService**: every other method answers `Unimplemented` until its row.
  It speaks Connect, gRPC and gRPC-Web over HTTP/1.1 and HTTP/2.
- **Timeouts**: `ReadHeaderTimeout` (10 s) and `IdleTimeout` (2 min, a
  connection with no request or open stream). No `ReadTimeout`: the agent
  streams are long-lived.
- **Re-issue** (`Ensure`): reason `names_changed` when the SANs differ from
  the wanted names, `renewal` at two thirds of the lifetime. The new
  certificate is signed by the stored CA and written with
  `statedb.ReplaceServerCert` (one writer transaction) before it is served; on
  a failure the old one stays in service and the next tick retries.
- No mDNS advertising yet (row 5); `discovery.mdns` is loaded and has no effect.

## Signals

| Signal | When |
|---|---|
| `fleet_hub_enroll_total{result}` | every Enroll: `enrolled`, `replayed`, `refused` (the caller's error), `failed` (the hub's) |
| `enroll.completed` (`machine_id`, `replayed`, `hostname_mismatch`) | a new or replayed enroll |
| `enroll.refused` (`reason`, `peer`) | a refusal; `reason` is the ErrorDetail reason without `REASON_CODE_` |
| `enroll.failed` (`step`, `err_kind`) | the hub's failure; `step` is `store`, `previous_machine`, `random`, `sign` or `encode` |
| `fleet_hub_auth_failures_total{reason="revoked"}`, `tls.client_cert_revoked` (`serial`, `machine_id`) | a handshake refused for a revoked serial |
| `fleet_hub_server_cert_issues_total{reason, result}`, `tls.server_cert_issued` (`reason`, `names` count, `not_after`), `tls.server_cert_issue_failed` (`reason`, `err_kind`, `not_after` still served) | server certificate re-issues |

No token, secret or part of its hash, no facts, no key, no certificate and no
SAN list is logged. A failed handshake is also a debug-level
`http.server_error` (`listener="agent"`, `kind="tls_handshake"`), and a
recovered handler panic an error-level one (`kind="panic"`), through
`telemetry.HTTPErrorLog`, never plain text on stderr.

Tests: `CGO_ENABLED=0 go test ./internal/hub/gateway/`. They dial the real
listener with `crypto/tls` and the generated gRPC client against a real
`state.db` and a real `revocation.List`: enroll (certificate CN and URI SAN,
chain, accepted as a client certificate), replay until 24 h past the TTL,
every refusal of the 02g table, a refusal that must not burn the token,
store failure, a token naming a previous machine, concurrent first enrolls
(one machine row), a revoked serial refused fresh and on a resumed session,
the server certificate refused as a client certificate, TLS 1.2 refused, and
renewal with a fake clock and a tick channel. The listener tests read one
clock, `wallNow`, for issuing and verifying, so they pass on any date.
