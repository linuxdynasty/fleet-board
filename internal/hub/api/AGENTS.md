# internal/hub/api

Read the root `AGENTS.md` first; it applies here. Load `fleet-go`,
`fleet-security` and `fleet-telemetry-coverage`; `fleet-contract-rigor` for
anything that touches `FleetService`. Rules for this package:

- The Host and Origin checks of 02f run before every handler on every
  listener this package serves; a new route goes behind them, never beside.
- Never send an `Access-Control-Allow-*` header, and never trust a proxy
  header (`X-Forwarded-For`, `X-Forwarded-Host`).
- Every refusal is the 02f Connect error body with its `ErrorDetail` reason
  and is counted by reason; reasons are the closed `telemetry.AuthReason` set.
- Every new RPC names its scope and has a denied-path test (02d).
- A change here updates this README and `doc.go` in the same commit.
