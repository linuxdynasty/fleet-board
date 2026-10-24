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
and other unbuilt RPCs return `UNIMPLEMENTED`. Watch streams per-topic
subscriptions with the initial state (02c); unserved topics (`ROUTINES`,
the v3 `DEVICES` and `NOTIFICATION_TARGETS`) are refused as whole
subscriptions.

The API and optional metrics listeners share one `Quotas` instance. Read and
write allowances belong to the fixed owner. `GetServerInfo` and `/metrics`
use the connection's canonical source IP, never a forwarding header or source
port. The peer table holds at most 1024 records. Each record has a canonical
IP key and two fixed counters; it stores no header, path or token text. A
full table keeps every peer's spent allowance and
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

`RevokeMachine` has a file write before its final database write. Its short,
non-mutating writer job checks replay, conflict, machine, and version, then
reserves one write allowance while that job still holds the database
transaction. The transaction ends before any revocation file I/O. A replay
and an already-revoked no-op each spend one allowance. A valid request that
finds no allowance does not stage serials, cut a stream, or write an audit or
request record. A canceled request before reservation is not admitted; once
reserved, the hub finishes staging and the final writer job even if the
client disconnects.

```text
request -> writer precheck: BEGIN IMMEDIATE -> row/replay/version checks
  -> quotaRequest.mu -> Quotas.mu -> reserve permit -> unlock quota/request
  -> COMMIT and release writer transaction
request -> List.Stage: List.mu across file append/fsync and in-memory set swap
  -> unlock List.mu
request -> final writer: BEGIN IMMEDIATE -> recheck mutable rows
  -> consume permit -> state.db changes and audit -> COMMIT
request -> reconcile new serials under List.mu -> Bye after commit
late version/conflict/unknown refusal -> refund permit under permit.mu,
  then Quotas.mu; staged serials stay revoked (fail closed)
```

The file lock never surrounds a writer job or a network call. The quota and
request locks never surround file I/O, a writer wait, or `Bye`. A refund can
credit only its original minute, and finalizing or refunding the same permit
again has no effect. The existing `fleet_hub_api_rate_limited_total` and
`config.machine_write_refused` signals show quota refusals; the existing
`config.machine_written` and `config.machine_write_failed` signals show the
result after admission.
