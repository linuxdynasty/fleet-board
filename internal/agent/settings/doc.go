// Package settings applies the hub's Settings on the agent (plan row 6, PR
// 6f; docs/specs/03e-settings-and-drift.md W10 and
// docs/specs/03e2-settings-merge-on-the-agent.md): it checks each field,
// merges it tighten-only under the policy file (the file can make a hub
// value stricter, never looser), keeps the result in agent.db before the
// SettingsAck is queued, and holds the effective values the executor and the
// Snapshot read.
//
// [Merge] is the pure merge of one message. [Manager] is the running part:
// [Manager.Hook] is conn.LoopOptions.Settings and only hands over; one
// [Manager.Run] goroutine orders, merges, saves and acks; [Open] merges the
// held settings again under the policy file loaded at start. The Manager is
// the executor's exec.Policy (safe mode, rate limits, local approval).
// Wiring into fleet-agent run is plan row 6i.
package settings
