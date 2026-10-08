# internal/hub/revocation

The hub's certificate revocation list
([02g](../../../docs/specs/02g-agent-security.md#hub-identity-and-restore)):
the `revoked-serials` file in the state folder, `cert_serials.revoked_time`
in `state.db`, and the in-memory set the agent listener checks at every TLS
handshake.

```go
list, err := revocation.Load(ctx, revocation.Options{Dir: stateDir, Store: st, Logger: log}) // at start, before the agent listener binds
if list.Revoked(ca.SerialHex(leaf.SerialNumber)) { /* refuse the handshake */ }
err = list.Revoke(ctx, []revocation.Entry{{Serial: s, NotAfter: end}}, now) // RevokeMachine (plan row 13a)
```

## The file

```text
# fleet-board revoked-serials v1
1f3a9c0e5b7d2a4c6e8f0a1b3c5d7e9f 2027-01-05T11:55:00Z
```

The header line, then one `<serial> <not_after>` line per revoked
certificate: the serial in lowercase hex without leading zeros
(`ca.SerialHex`), one space, the certificate's `not_after` in RFC 3339.
Mode 0600, owned by the user running `fleetd`; the file lives outside
`state.db` so a restore from an older backup re-applies it.

## Load (at start)

1. Remove a `.revoked-serials.tmp-*` rewrite a crash left behind.
2. Read the file. A missing file is normal. A loose mode or another owner is
   `statedb.ErrLooseMode` (start reason `mode_loose`). A symlink, a folder,
   an unreadable file, a file over 16 MiB, a header other than v1, or any
   complete line that is not the format (a blank line, upper-case hex, a bad
   time, a third field) is `ErrInvalid` (start reason
   `revocations_invalid`); the error names the line number, never the line.
3. A last line with no newline is a torn write: it is dropped and
   `hub.revocations_torn_line` is logged at warn level.
4. Reconcile both ways: serials revoked in `state.db` but missing from the
   file go into the file; serials in the file are marked revoked in
   `state.db` (a serial `state.db` does not know stays in the file only).
   Expired lines are dropped.
5. If the file needs any change, rewrite it atomically: a 0600 temporary
   file, fsync, rename, fsync of the folder. A missing file with nothing to
   list stays missing.

The in-memory set is the union of the file and `state.db`.

## Revoke

In this order, under one mutex: append the lines and fsync the file (a new
file gets the header and an fsync of the folder); update the in-memory set
(copy on write, one atomic pointer swap); then `statedb.RevokeSerials`. A
file failure changes nothing else and makes the next `Revoke` rewrite the
whole file instead of appending after a possibly torn line. A `state.db`
failure is returned with the serial already revoked in the file and in
memory: revocation fails closed, and the next start reconciles `state.db`.
Row 3 builds this path; `RevokeMachine` (plan row 13a) calls it.

## Signals

`hub.revocations_torn_line` (warn, no fields). A refused start is the
daemon's `hub.start_failed` with reason `revocations_invalid` or
`mode_loose`. The handshake refusal is the gateway's
`tls.client_cert_revoked` and `fleet_hub_auth_failures_total{reason="revoked"}`.

Tests: `CGO_ENABLED=0 go test ./internal/hub/revocation/`. They use a real
state folder and `state.db`: a missing file, both-way reconcile with the
exact rewritten text, each torn case, each bad line, a loose mode, a folder in
the file's place, a leftover rewrite, the revoke order, a closed store (fails
closed), and a failed append followed by a rewrite.
