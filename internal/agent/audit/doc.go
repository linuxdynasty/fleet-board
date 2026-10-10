// Package audit is the agent's local audit log (01f M1): audit.log in the
// agent state folder, one JSON object per line, the machine-side rows of the
// 02g audit table (command.refused, command.local_approval,
// machine.local_drain, machine.local_undrain, policy.changed,
// policy.safe_mode). The hub keeps the permanent copy; this file is what the
// owner can read on the machine when the hub is out of reach.
//
// Every field is a closed value: a time, one of six action names, a command
// id, a command kind, a reason and status code, the original requester, and
// at most four detail pairs (old and new hash; approved or denied and cli or
// notification). A message, a path or file text cannot be written: [Log.Append]
// refuses a field that is not letters, digits and _ . : - up to 128 bytes.
//
// The file is opened for append with mode 0600, synced after each line, and
// rotated at 10 MiB keeping three files (audit.log, audit.log.1, audit.log.2).
// A file deleted or replaced under the open handle is reopened. A write that
// fails never blocks the caller: it is counted in
// fleet_agent_audit_write_failures_total and logged as audit.write_failed at
// most once a minute. Doctor never parses audit.log.
package audit
