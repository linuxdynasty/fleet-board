# internal/hub/api

The hub's HTTP API ([02b](../../../docs/specs/02b-hub-api.md),
[02f](../../../docs/specs/02f-hub-authentication.md#host-and-origin-checks)).
Today it serves only `/metrics`, behind the Host and Origin checks.

```go
srv := api.NewServer(tel, "api", boundPort) // api.Handler plus timeouts and the JSON ErrorLog
```

`NewServer` sets `ReadHeaderTimeout` (10 s) and `IdleTimeout` (2 min, a
keep-alive connection with no request), no `ReadTimeout` (Watch and attach will
be long streams), and `ErrorLog` through `telemetry.HTTPErrorLog`, so net/http's
own error lines are debug-level `http.server_error` events, not stderr text.

- **Host check first.** The `Host` header must be `localhost`, `127.0.0.1` or
  `[::1]` with the bound port (case-insensitive; the bare name too on port
  80). Anything else is HTTP 403, a Connect `permission_denied` error whose
  `ErrorDetail.reason` is `HOST_NOT_ALLOWED`. This is what stops DNS
  rebinding.
- **Origin check next.** A request that sends `Origin` must send exactly
  `http://` plus one of those host-and-port pairs, or it gets 403 with
  `ORIGIN_NOT_ALLOWED` (`null`, `https://` and other ports are refused).
- **Then the routes:** `GET /metrics` (the telemetry handler, which also
  admits only a loopback peer), and 404 for every other path. No
  `FleetService`, no `/healthz`, no CORS header.
- Refusals count in `fleet_hub_auth_failures_total{reason="host"|"origin"}`.
- The same handler serves the optional `listen.metrics` listener, with that
  listener's port.
- `local_trusted` only; the allow-lists of authenticated mode
  (`public_hosts`, `public_origins`) are refused by config until v3. The admin
  socket server (plan row 2b, second PR) and `FleetService` arrive here.

Tests: `CGO_ENABLED=0 go test ./internal/hub/api/`. They decode the refusal
body down to the `ErrorDetail` reason and check the counter in a real scrape.
