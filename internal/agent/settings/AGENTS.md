# internal/agent/settings

Read the root `AGENTS.md` first; it applies here. Load `fleet-go`,
`fleet-security`, `fleet-sqlite-store` and `fleet-telemetry-coverage`.
Rules for this package:

- Tighten only. The policy file may make a hub value stricter, never looser,
  and a hub value is never let past a local opinion. Every merge row has a
  table test with the hub looser and stricter; a new field gets both.
- `Merge` stays a pure function. The store job (`ring.SaveSettings`) commits
  before the `SettingsAck` is queued; no lock is held across it or the send.
- `Hook` runs on the connection loop's sending goroutine: it only puts the
  message in the intake and never waits. Send the ack from `Run`'s goroutine
  with a context that is not the hook's.
- Logs and metric labels carry field names, reasons, generations and hashes;
  never a list value (it may hold paths) or an account id as a label.
- A change here updates this README, `doc.go`, 03e2 and, for the store, 02i
  in the same commit.
