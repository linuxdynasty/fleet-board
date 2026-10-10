# internal/agent/audit

The agent's local audit log (M1, [01f](../../../docs/specs/01f-agent-control-goals-and-policy.md)):
`audit.log` in the state folder. The hub's audit table is the permanent record
([02g](../../../docs/specs/02g-agent-security.md)); this file is the copy on the
machine.

```go
l, err := audit.New(stateDir, audit.Options{Telemetry: tel}) // does not touch the file
l.Append(audit.Entry{
    Action: audit.PolicyChanged,
    Detail: map[string]string{"old_hash": old, "new_hash": new},
})
```

- One JSON line per entry, mode 0600, `fsync` per line.
- Closed fields only. `Append` refuses an unknown action, and a field that is
  not `[A-Za-z0-9_.:-]{0,128}`, so a message, a path or file text cannot land
  in the file. It counts that as `err_kind="constraint"`.
- Rotates at 10 MiB and keeps three files: `audit.log`, `.1`, `.2`.
- A failed write returns nothing and blocks nothing. It adds to
  `fleet_agent_audit_write_failures_total{err_kind}` and logs `audit.write_failed`
  at most once a minute (with a `suppressed` count).
- The first `Append` opens the file, so a folder that cannot be written does not
  stop the agent from starting. A deleted or replaced file is reopened; a torn
  last line is ended before the next entry.
- `run` holds `fleet-agent.lock`, so one process writes the file.

Today the writers are the policy loader (`policy.safe_mode` at load,
`policy.changed` from the watcher). The executor adds `command.refused`, the
approver `command.local_approval`, and drain the two `machine.local_*` rows.

The local `policy.safe_mode` row differs from the hub's (02g): it carries
`reason=<code>` (`unknown_key`, `bad_type`, `out_of_range`, `unreadable` and so
on) and `detail.hash`, never the one-line `PolicyInfo.error`, because the local
fields are closed values. The one-line error is in the snapshot and in the
`policy.safe_mode` log line; the hub writes its own row from the snapshot.
