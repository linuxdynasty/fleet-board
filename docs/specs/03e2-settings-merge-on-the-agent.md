# 03e2. Settings: the merge on the agent

Part of [03e](03e-settings-and-drift.md) (W10). 03e says what each setting
means and which way the merge goes. This file says how `fleet-agent` applies a
`Settings` message: the checks, the layer it names, what it keeps in
`agent.db`, and the signals it leaves. Plan row 6, PR 6f
(`internal/agent/settings`).

## The rule in one line

The hub can only make a setting stricter than `policy.yaml`, never looser.
Where the file says nothing, the hub's value stands; where neither says
anything, the built-in default ([defaults](defaults.md)) does.

## Steps for one `Settings`

1. The connection loop hands the message to the settings intake (8 places)
   and goes on; a full intake drops it (`command.intake_full`, kind
   `settings`) and the hub sends it again.
2. The worker re-hashes `policy.yaml` (the event-driven safety net of 03e;
   the loaded policy still changes only at a restart).
3. Order, by `(settings_epoch, generation)` ([02](02-contracts.md) "Settings
   order"): no epoch is ignored with no ack; the held generation again, or a
   lower one in the same epoch, is answered with the held `SettingsAck` and
   nothing is merged or saved; a new epoch is taken whatever its generation.
4. Check each field. A value out of range ([03e](03e-settings-and-drift.md)
   key table: `max_active` 1 to 16, `max_total` 1 to 32, `max_windows` at least
   1, durations above zero, `cert_lifetime` up to 365 days, `default_server` one
   of the three modes, harness names and labels by their pattern, absolute
   repo roots, known command kinds each named once, `per_minute` at least 1, a
   template with no control characters and at most 256 bytes) is rejected with
   `INVALID_ARGUMENT` and that field keeps the value kept before it. A probe
   interval is checked per account (`probe_interval_by_account.<id>`).
   `merged_grace` is the hub's own and is echoed unchecked.
5. Merge field by field (below). In safe mode nothing is merged: every field
   the message states is rejected with `POLICY_SAFE_MODE`, the ack carries the
   built-in defaults, and steps 6 and 7 are skipped except queuing the ack.
6. Save the kept `Settings`, the `SettingsAck` and the loaded policy hash in
   one `agent.db` transaction ([02i](02i-delivery-and-sequencing.md) "What
   agent.db keeps", `settings_held`). A failed save sends no ack; the hub sends
   the settings again. Nothing is saved in safe mode.
7. Swap the effective values the executor and the `Snapshot` read, then queue
   the `SettingsAck`. A lost ack comes back at the next `Hello`: when the
   hub's generation for the machine equals `Hello.settings_generation` in the
   same epoch and the hub holds no `SettingsAck` for it, the hub re-sends that
   `Settings` once on the new stream, without waiting for the 60 s resend
   (`HelloAck.settings_generation`, agent.proto), and step 3 answers the
   repeat with the held ack, rejections included. The hub stores an ack only
   if its generation is not lower (03e), so the repeat changes nothing else.
   The hub side is plan row 5 (settings send and `SettingsAck`); until it
   lands, a lost ack for generation N comes back only with generation N+1,
   though the effective values still reach the hub in `Snapshot.settings`.

In safe mode nothing is kept: Hello goes on reporting the generation held
before, so once the owner fixes the file and restarts, the hub sends its
current Settings at the first connect (HelloAck, agent.proto) and the agent
merges and checks it fresh. Until then the held generation applies under the
fixed file.

## The merge

| Kind of field | Fields | Effective value | Capped when |
|---|---|---|---|
| a number or duration where less is stricter | `max_active`, `max_total`, `max_windows`, `cert_lifetime`, `attach_idle_timeout`, `spool_cap_bytes` | `min(hub, local)` | the local value is below the hub's |
| a floor | `probe_interval` and each account's interval | `max(hub, local min_probe_interval)` | the floor is above the hub's (named `probe_interval`) |
| a mode | `default_server` | the stricter, `NONE < OBSERVE < CONTROL` | the local mode is stricter |
| a requirement | `block_on_battery`; `local_approval` per kind | hub or local | the file requires what the hub did not |
| a limit per kind | `rate_limits` | `min` per kind; a kind only one layer names keeps that value | a local limit is below the hub's for any kind |
| a set | `allowed_harnesses`, `allowed_repo_roots` | `hub ∩ local`, in the hub's order | the result is smaller than the hub's list |
| not a safety field | `window_name_template`, `idle_threshold`, `share_identity_email` | the hub's; else the file's; else the default | never |
| hub only | `merged_grace` | echoed unchanged | never |
| labels | `Settings.labels` | `hub ∪ local`, sorted | never |

On the wire an empty list cannot be told from an absent one, so an empty hub
list is no opinion, so the hub cannot say "allow no harness" through
`allowed_harnesses`; draining the machine is the tool for that. In `policy.yaml` an absent list is no opinion and `[]` is
one (none), except `allowed_repo_roots`, where absent is empty too (03e): so
the hub alone never adds a repo folder. Repo roots match exactly after
cleaning the path, never as a sub-folder, so a symlink inside a local root
cannot lead out of it. With no opinion anywhere `allowed_harnesses` is the
names the contract knows (03e), with no source entry.

Each effective field names its `SettingSource.layer`: `LOCAL_CEILING` when
the file's value is in effect (capped, or the hub said nothing; only a cap is
in `capped_fields`), `AGENT_DEFAULT` for a built-in default, and `HUB` when
the hub's value stood: `Settings` carries resolved values only, so the hub
replaces `HUB` with the fleet, group or machine layer when it stores the ack
and the snapshot (03e). A field the agent rejected is in
`rejected`, never in `capped_fields`. Field numbers the agent's schema does
not know go in `unrecognized_value_fields` and `unrecognized_settings_fields`.

## At start

`fleet-agent` merges the held `Settings` again under the policy file it just
loaded. The held message already has each invalid field replaced, so this
merge finds nothing to reject; the new ack's `rejected` is the saved ack's
list, then any field this merge rejects that the list does not name (a newer
binary's stricter check). No check of a value reads the policy file, so a
rejection does not change with it, and the hub's `rejected_settings` goes on
showing it (03e failure table). When the policy hash differs from the one
saved, it saves the new ack for the same generation and logs
`settings.reevaluated`; the next `Snapshot.settings` shows the new effective
values (03e state machine, CEILING_CHANGED). Under the same hash the saved ack
stands. A field that kept its value from an older generation is named `HUB`,
as it was before the restart; mapping it to its hub layer is the hub's (plan
row 5). In safe mode it saves nothing: the effective values are the built-in
defaults at generation 0, every field is rejected with `POLICY_SAFE_MODE`
(the saved rejections included), and the held row waits for the next normal
start.

## Concurrency

```text
sending goroutine ──Hook (never waits)──▶ intake (8) ──▶ worker (one goroutine)
                                                        │ re-hash policy.yaml
                                                        │ merge (pure)
                                                        │ agent.db: one write job (settings_held)
                                                        │ swap the effective view (atomic pointer)
                                                        ▼ Loop.Send(SettingsAck): may wait for room,
                                                          ends with the stream it came on
executor, Snapshot ──read the effective view (no lock)──┘
```

No lock is held across the store job or the send; the transaction holds no
network call. The worker is the only writer of `settings_held`.

## Telemetry

| Signal | When |
|---|---|
| `settings.applied{epoch, generation, capped, rejected, unrecognized, safe_mode}` (info) | a `Settings` was merged, and saved unless in safe mode |
| `settings.capped{field, hub_value, local_ceiling}` (info) | the file lowered a field; the two values only for a number, duration or mode, never for a list (it may hold paths) |
| `settings.rejected{field, reason}` (warn) | a field was not applied |
| `settings.ignored{reason, generation, held_generation}` (info) | `reason` is `repeat`, `older` or `no_epoch` |
| `settings.reevaluated{old_hash, new_hash, generation, capped}` (info) | at start, the held settings merged under a changed policy file |
| `settings.store_failed{err_kind}` (warn) | the save failed; no ack was sent |
| `settings.ack_unsent{err_kind, generation}` (info) | the ack could not be queued (`no_stream`, `queue_full`, `ended`, `other`); the hub re-sends at the next `Hello` when it holds no ack for the generation (step 7) |
| `fleet_agent_settings_applied_generation` (gauge) | the generation held, 0 before any |
| `fleet_agent_settings_capped_total{field}` | fields the file lowered, at each merge |
| `fleet_agent_settings_rejected_total{field, reason}` | fields not applied |
| `fleet_agent_settings_ignored_total{reason}` | messages not applied |
| `fleet_agent_settings_store_failed_total` | saves that failed |
| span `agent.settings.merge{generation}` | the merge, a child of `Settings.trace` |

At 3 AM: "why does this machine ignore the group setting?" →
`settings.capped` for that field (the file is stricter), or
`settings.rejected` (the value was out of range, or the agent is in safe
mode), or `fleet_agent_settings_applied_generation` below the hub's (never
applied: look for `settings.store_failed` or `settings.ignored`).
