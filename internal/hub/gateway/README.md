# internal/hub/gateway

The agent listener (`listen.agent`, [02j](../../../docs/specs/02j-hub-runtime.md#listeners)):
TLS 1.3 with the CA-issued server certificate, serving `AgentService`.

```go
certs, err := gateway.NewCerts(gateway.CertsOptions{Store: st, Identity: id, Names: names, Telemetry: tel})
err = certs.Ensure(ctx)          // at start: re-issue if the names changed or it is past two thirds
go certs.Run(ctx, ticker.C)      // hourly in fleetd run; stops with ctx
srv := gateway.NewServer(certs)  // srv.ServeTLS(ln, "", "")
```

- **TLS 1.3 only**, with the certificate from an atomic pointer, so a
  re-issue takes effect on the next handshake without a restart.
- **Client certificates** are optional (an enrolling agent has none) and,
  when sent, must chain to the hub CA (`VerifyClientCertIfGiven`). Checking the
  certificate per RPC and revocation arrive with plan rows 3 and 5.
- **AgentService**: the generated Connect handler, every method
  `Unimplemented` for now (Enroll and RenewCert in row 3, Connect, Terminal and
  SessionLog in rows 5 and later). It speaks Connect, gRPC and gRPC-Web over
  HTTP/1.1 and HTTP/2.
- **Re-issue** (`Ensure`): reason `names_changed` when the SANs differ from
  the wanted names, `renewal` at two thirds of the lifetime. The new
  certificate is signed by the stored CA and written with
  `statedb.ReplaceServerCert` (one writer transaction) before it is served; on
  a failure the old one stays in service and the next tick retries.
- No mDNS advertising yet (row 5); `discovery.mdns` does nothing.

## Signals

`fleet_hub_server_cert_issues_total{reason, result}` (`reason`:
`names_changed`, `renewal`; `result`: `ok`, `failed`) and the log events
`tls.server_cert_issued` (`reason`, `names` count, `not_after`) and
`tls.server_cert_issue_failed` (`reason`, `err_kind`, `not_after` of the
certificate still served). No key and no name list is logged.

Tests: `CGO_ENABLED=0 go test ./internal/hub/gateway/`. They dial the real
listener with `crypto/tls` (TLS 1.3, a TLS 1.2-capped client refused, client
certificates from the hub CA and from another CA), call every method with the
generated client over Connect and gRPC, and drive renewal with a fake clock and
a tick channel.
