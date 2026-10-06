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
  `not_after` only.
- Tests dial the real listener; a fake clock and a tick channel drive renewal.
- A change here updates this README and `doc.go` in the same commit.
