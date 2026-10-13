package statedb

// approvalsSQL is migration 12: the hub's owner decisions and session
// control (row 19: 03d W7, 02m). approvals holds one row per Approval: a
// decision only a person may make. approval_inflight holds one row per
// prompt with an answer on its way to the agent, so a second answer for
// the same prompt is refused until the result lands (03d "one answer in
// flight"). hub_sessions records the sessions the hub started (their ids
// are assigned before the command is sent), so KillWindow can tell the
// fleet's own windows from the developer's. request_log maps (principal,
// request_id) to the command a mutating session call sent, so a retry
// returns the stored Operation instead of sending twice (02b idempotency).
const approvalsSQL = `CREATE TABLE approvals(
	id TEXT PRIMARY KEY CHECK (id <> ''),
	version INTEGER NOT NULL CHECK (version >= 1),
	kind TEXT NOT NULL CHECK (kind IN ('PROMPT_ANSWER', 'COMMAND_CONFIRM', 'DISPATCH_CONFIRM')),
	state TEXT NOT NULL CHECK (state IN ('PENDING', 'DECIDED', 'EXPIRED', 'WITHDRAWN')),
	severity TEXT NOT NULL CHECK (severity <> ''),
	preview TEXT NOT NULL CHECK (length(preview) <= 2048),
	machine_id TEXT NOT NULL DEFAULT '',
	session_id TEXT NOT NULL DEFAULT '',
	pane_server TEXT NOT NULL DEFAULT '',
	pane_server_start_id TEXT NOT NULL DEFAULT '',
	pane_id TEXT NOT NULL DEFAULT '',
	goal_id TEXT NOT NULL DEFAULT '',
	triage_item_id TEXT NOT NULL DEFAULT '',
	prompt_id TEXT NOT NULL DEFAULT '',
	command BLOB,
	allowed TEXT NOT NULL DEFAULT '[]',
	default_choice TEXT NOT NULL DEFAULT '',
	fallback TEXT NOT NULL CHECK (fallback IN ('DENY')),
	expire_time INTEGER NOT NULL,
	required_role TEXT NOT NULL CHECK (required_role <> ''),
	decided_by_id TEXT NOT NULL DEFAULT '',
	decided_by_kind TEXT NOT NULL DEFAULT '',
	decided_by_name TEXT NOT NULL DEFAULT '',
	decision TEXT NOT NULL DEFAULT '',
	decided_time INTEGER,
	operation TEXT NOT NULL DEFAULT '',
	decide_principal TEXT,
	decide_request_id TEXT,
	decide_request_hash BLOB,
	create_time INTEGER NOT NULL,
	update_time INTEGER NOT NULL,
	hub_seq INTEGER NOT NULL,
	observed_time INTEGER NOT NULL,
	UNIQUE (decide_principal, decide_request_id)
) STRICT;
CREATE TABLE approval_inflight(
	prompt_key TEXT PRIMARY KEY CHECK (prompt_key <> ''),
	approval_id TEXT NOT NULL REFERENCES approvals(id),
	held_time INTEGER NOT NULL
) STRICT;
CREATE TABLE hub_sessions(
	session_id TEXT PRIMARY KEY CHECK (session_id <> ''),
	machine_id TEXT NOT NULL CHECK (machine_id <> ''),
	create_time INTEGER NOT NULL
) STRICT;
CREATE TABLE request_log(
	principal_id TEXT NOT NULL CHECK (principal_id <> ''),
	request_id TEXT NOT NULL CHECK (request_id <> ''),
	command_id TEXT NOT NULL DEFAULT '',
	approval_id TEXT NOT NULL DEFAULT '',
	request_hash BLOB NOT NULL,
	create_time INTEGER NOT NULL,
	PRIMARY KEY (principal_id, request_id)
) STRICT;`
