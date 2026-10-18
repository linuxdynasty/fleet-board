# Local TCP API

The API listener admits the fixed `local-owner` USER after checking the
loopback peer, Host, Origin, and unsupported credential carriers. Any present
`Authorization` header or exact `__Host-fleet_session` cookie fails closed.
The Host must be `localhost`, `127.0.0.1`, or `[::1]` with the bound port
(case-insensitive, plus the bare name on port 80); another Host is HTTP 403
`HOST_NOT_ALLOWED`, which stops DNS rebinding.
Unrelated cookies, including `__Host-fleet_oauth`, grant no identity. The
local approval secret still reaches each protected call's existing check.
HTTP limits request headers to 1 MiB; this bounds raw cookie-name scanning
without turning a large unrelated cookie into a sign-in credential.
Connect calls need a Connect protocol header or gRPC content type. The local
mode returns `FAILED_PRECONDITION` for sign-in and device RPCs. API tokens
and other unbuilt RPCs return `UNIMPLEMENTED`; Watch remains unbuilt.

The API and optional metrics listeners share one `Quotas` instance. Read and
write allowances belong to the fixed owner. `GetServerInfo` and `/metrics`
use the connection's canonical source IP, never a forwarding header or source
port. The peer table holds at most 1024 records, about 100 KiB of counters,
IP keys and map slots. A full table keeps every peer's spent allowance and
refuses a new peer until one record's debt expires. It never evicts a live
allowance to make room. There is no second request quota from this table size.

The handler owns validation and calls `quotaCheckpoint` after all higher
priority errors but before its first state change. This short call locks the
quota counters only; it releases that lock before a file write or network
call. A writer may call it inside a transaction after its checks. The writer
still owns every `state.db` mutation. A request context records its one
charge so a replay path cannot spend the allowance twice.

```text
TCP request context: quotaRequest.mu -> Quotas.mu -> release both
Writer after its row checks: open state.db transaction -> quotaRequest.mu
  -> Quotas.mu -> release both -> change row -> commit
Other requests: never hold Quotas.mu while waiting for the writer or network
```

The quota table has no database connection. No path takes a quota lock and
then waits for a store transaction, so the lock order cannot cycle.
