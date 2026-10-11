# internal/agent/settings

The agent side of settings (03e W10, [03e2](../../../docs/specs/03e2-settings-merge-on-the-agent.md)):
check each field of the hub's `Settings`, merge it tighten-only under
`policy.yaml`, keep it in `agent.db`, and answer with `SettingsAck`.

```go
m, err := settings.Open(ctx, settings.Options{DB: db, Local: pol, Sender: loop, Telemetry: tel})
loopOpts.Settings = m.Hook             // never waits; false when the intake (8) is full
go m.Run(ctx)                          // order, merge, save, then queue the ack
_ = m.Snapshot(ctx, snap)              // Snapshot.settings
n, ok := m.RateLimit("SendKeys")       // the executor's Policy: SafeMode, RateLimit, ApprovalRequired
out := settings.Merge(settings.Input{Hub: s, Prev: kept, Local: pol.Policy(), SafeMode: pol.SafeMode()})
```

- **Order.** `(settings_epoch, generation)`: a repeat or a lower generation
  in the same epoch is answered with the held ack and saves nothing; a new
  epoch is taken whatever its generation; no epoch is ignored.
- **Checks.** An out-of-range value is rejected (`INVALID_ARGUMENT`) and the
  field keeps the value kept before it. In safe mode every field is rejected
  (`POLICY_SAFE_MODE`) and only the defaults are in effect, but the hub's
  values are still kept, so a fixed file applies them at the next start.
- **Restart.** `Open` merges the held settings under the policy loaded now
  and saves the new ack when it changed (`settings.reevaluated`).
- **Layers.** `LOCAL_CEILING` when the file's value is in effect,
  `AGENT_DEFAULT` for a built-in default; a hub value is unnamed for now
  (`SETTING_LAYER_UNSPECIFIED`, an open question of PR 6f).
