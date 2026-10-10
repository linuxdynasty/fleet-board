package statedb

// commandsSQL is migration 8 (plan row 5, PR 5f): the command outbox and
// the Operation it is, in one row (row 5 ruling, fork 5), and the results
// beside it (03f data model). internal/hub/outbox owns every query.
//
//   - commands: one row per command_id (unique: the 03f race table's
//     backstop for a command delivered on two epochs). id is the send order
//     per machine; AUTOINCREMENT never reuses one. command is the encoded
//     agent Command with epoch and seq unset: both are set when it is sent,
//     per connection epoch, and the send is recorded in sent_epoch,
//     sent_seq and sent_time, unique per machine so a seq is never issued
//     twice in one epoch. state is the outbox (02-contracts: pending,
//     delivered, done, expired, failed) and op_state the Operation (hub/v1
//     OperationState); a closed outbox row is a DONE Operation and nothing
//     else is. status and reason are the Status and ReasonCode names
//     without their prefixes. version rises on every change (Operation is
//     observed-only, common.proto) and hub_seq is its Watch stamp. Like
//     machine_connections it has no foreign key to machines, so the
//     history outlives DeleteMachine; the outbox checks the machine exists.
//     The partial index holds only pending rows: the sender's read per
//     machine in send order and the outbox gauges' count.
//   - command_results: the first CommandResult of each command, with its
//     free text already scrubbed; triggers refuse an update and an insert
//     over a stored one (INSERT OR REPLACE deletes without firing delete
//     triggers). A row goes only with its command (the prune, plan row 5g).
const commandsSQL = `CREATE TABLE commands(
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	command_id TEXT NOT NULL UNIQUE CHECK (length(command_id) BETWEEN 1 AND 64),
	machine_id TEXT NOT NULL CHECK (machine_id <> ''),
	kind TEXT NOT NULL CHECK (kind <> ''),
	command BLOB NOT NULL,
	request_id TEXT NOT NULL DEFAULT '',
	goal_id TEXT NOT NULL DEFAULT '',
	principal_id TEXT NOT NULL DEFAULT '',
	create_time INTEGER NOT NULL,
	not_after_time INTEGER NOT NULL CHECK (not_after_time > create_time),
	state TEXT NOT NULL DEFAULT 'pending'
		CHECK (state IN ('pending', 'delivered', 'done', 'expired', 'failed')),
	op_state TEXT NOT NULL DEFAULT 'PENDING' CHECK (op_state IN ('PENDING', 'DELIVERED', 'RUNNING', 'DONE')),
	status TEXT NOT NULL DEFAULT '',
	reason TEXT NOT NULL DEFAULT '',
	step TEXT NOT NULL DEFAULT '',
	step_index INTEGER NOT NULL DEFAULT 0 CHECK (step_index >= 0),
	step_count INTEGER NOT NULL DEFAULT 0 CHECK (step_count >= 0),
	message TEXT NOT NULL DEFAULT '' CHECK (length(CAST(message AS BLOB)) <= 1024),
	sent_epoch INTEGER CHECK (sent_epoch BETWEEN 1 AND 9223372036854775806),
	sent_seq INTEGER CHECK (sent_seq >= 1),
	sent_time INTEGER,
	ack_time INTEGER,
	done_time INTEGER,
	version INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1),
	update_time INTEGER NOT NULL,
	hub_seq INTEGER NOT NULL CHECK (hub_seq > 0),
	CHECK ((sent_epoch IS NULL) = (sent_seq IS NULL) AND (sent_seq IS NULL) = (sent_time IS NULL)),
	CHECK ((op_state = 'DONE') = (state IN ('done', 'expired', 'failed'))),
	UNIQUE (machine_id, sent_epoch, sent_seq)
) STRICT;
CREATE INDEX commands_pending ON commands(machine_id, id) WHERE state = 'pending';
CREATE TABLE command_results(
	command_id TEXT PRIMARY KEY REFERENCES commands(command_id) ON DELETE CASCADE,
	machine_id TEXT NOT NULL CHECK (machine_id <> ''),
	epoch INTEGER NOT NULL CHECK (epoch >= 1),
	status TEXT NOT NULL CHECK (status <> ''),
	reason TEXT NOT NULL,
	message TEXT NOT NULL CHECK (length(CAST(message AS BLOB)) <= 1024),
	result BLOB NOT NULL,
	received_time INTEGER NOT NULL
) STRICT;
CREATE TRIGGER command_results_keep_first BEFORE UPDATE ON command_results
BEGIN SELECT RAISE(ABORT, 'a command keeps its first result'); END;
CREATE TRIGGER command_results_no_replace BEFORE INSERT ON command_results
WHEN EXISTS (SELECT 1 FROM command_results WHERE command_id = NEW.command_id)
BEGIN SELECT RAISE(ABORT, 'a command keeps its first result'); END;`
