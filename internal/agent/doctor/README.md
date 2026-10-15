# internal/agent/doctor

The checks behind `fleet-agent doctor` (plan row 6, M3). Spec: [01f](../../../docs/specs/01f-agent-control-goals-and-policy.md)
M3, [01 C3](../../../docs/specs/01-agent-capabilities.md), [03 W1](../../../docs/specs/03-workflows.md),
[03e](../../../docs/specs/03e-settings-and-drift.md), [02k](../../../docs/specs/02k-v1-surface-and-cli.md).

## Use

```go
rep := doctor.Run(ctx, doctor.Options{StateDir: s, ConfigDir: c, ProbeDir: p, TmuxVersion: tmux}, doctor.Local()...)
doctor.WriteText(os.Stdout, rep) // or doctor.WriteJSON
if rep.Failed() { /* exit 1 */ }
```

A new check is a `doctor.Check{ID, Title, Run}`; append it to the list passed to
`Run`. A check this build cannot run yet is `doctor.NotCheckedYet(id, title, row)` (or `NotCheckedYetAt(id, title, label)`).

## Statuses

`ok`, `warn`, `fail`, `unknown` (the fact is not there: a missing or old status
file, a tool that would not run), `not_checked` (no code for it yet; the reason
names the row). Exit 1 only for `fail`. Every check has a reason; every check
that is not ok or not_checked has a fix line.

## The local checks, in print order

| ID | Looks at | Fails when |
|---|---|---|
| `state_dir` | the state folder: a real folder, yours, no group/other access | missing, a link, another owner, mode over 0700 |
| `config_dir` | the config folder (prints the one used) | not a folder, another owner, every user can write (warns when only its group can) |
| `identity` | `identity.json`: present, private, whole (the key is never decoded) | missing, loose, damaged |
| `cert_expiry` | the certificate that ends last (identity or a valid `cert.pem`) | expired; warns past two thirds of its life |
| `cert_file` | `cert.pem`: private, a certificate for this machine signed by the pinned CA; the key block is never read | loose (fail); damaged or wrong machine or CA (warn: the enrolled pair is used) |
| `renew_key` | `renew.key`: presence and mode only | open to other users (renewal is blocked) |
| `policy_file` | `policy.yaml` read fresh: missing (normal), valid, safe mode with its one-line error | safe mode, or writable by every user (warns when only its group can write it) |
| `policy_roots`, `policy_harnesses` | the defaults in force | warns on empty roots or an explicit empty harness list |
| `policy_watch` | `policy_watch: off` in status.json | warns when off |
| `status_file` | `status.json` age (old after 2 minutes) | warns when old or damaged |
| `connection` | state, peer, last failure per address; `ended.json` (read-only, once) and the certificate for the end the loop stopped on; status.json's end is only a note | fails when `ended.json` names this machine (revoked, unknown machine, fleet mismatch), is damaged, or the certificate that ends last is expired; a leftover for another machine, or an end status.json names that `ended.json` no longer does, is `unknown` |
| `push_socket` | `push_off` in status.json: the network watcher's push socket (ADR 0029 decision 6) | warns when off (the agent then polls every 15 seconds); unknown when the file is old |
| `mdns_browse` | the last mDNS browse in status.json (its own time, kept when the file is old) | warns when it found no hub or had no multicast interface; unknown before any browse |
| `snapshot_size` | `snapshot_oversize` | warns: the hub must be upgraded |
| `clock` | hub time minus this clock at the last hello | warns over 5 minutes, fails over 15 |
| `enroll` | `enroll.json`: the last failed enroll's code and reason | fails when a failure is recorded |
| `tmux` | `tmux -V` through `machine.TmuxVersion` | missing or older than 3.2 |
| `probe_folder` | the usage probe's folder: exists, private, empty (names inside are never printed) | warns |
| `hub_handshake` | a TLS handshake against the pinned CA, never a `Connect` call | warns when unreachable, fails when untrusted; unknown with no identity or address |
| `linger` | `loginctl show-user --property=Linger` on Linux (ok on other systems) | fails when off; unknown when unreadable |
| `probe_trust` | the probe folder trust per harness: each installed harness starts once in the probe folder on a throwaway tmux server, the first screen is read through the probe's trust detection, no key is sent and the session is torn down | fails when a harness shows its trust screen (the fix is the one-time step per harness, 01 C3); unknown when a screen could not be read or proved nothing (ok needs every installed harness at a proven ready prompt) |
| `harness_paths`, `two_pane_folders` | nothing yet | `not_checked` (rows 8 and 9) |

## Not here

The per-harness trust steps live in the `probe_trust` fix lines (01 C3).
Doctor never removes `ended.json`, even
a leftover for another machine: `run` does that.
