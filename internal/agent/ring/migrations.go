package ring

import "github.com/linuxdynasty/fleet-board/internal/store"

// Migrations is the ordered, forward-only schema of agent.db. Never edit an
// entry that has shipped; append the next one.
func Migrations() []store.Migration {
	return []store.Migration{{
		Version: 1,
		Name:    "agent_state",
		SQL:     agentStateSQL,
	}, {
		Version: 2,
		Name:    "command_log",
		SQL:     commandLogSQL,
	}, {
		Version: 3,
		Name:    "row_guards",
		SQL:     rowGuardsSQL,
	}, {
		Version: 4,
		Name:    "ack_guards",
		SQL:     ackGuardsSQL,
	}, {
		Version: 5,
		Name:    "command_body",
		SQL:     commandBodySQL,
	}, {
		Version: 6,
		Name:    "event_ring",
		SQL:     eventRingSQL,
	}}
}

// eventRingSQL is the offline event ring (02i "Queue and ring rules", 03n
// W2 "Persistence"): NEVER_DROP and COALESCE events, encoded whole, in seq
// order. coalesce_key is ” for NEVER_DROP; one entry per non-empty key.
// created_time (agent clock, ms) drives the one-week cap and bytes the
// 64 MiB cap (defaults.md). event_ring_size keeps the count and the bytes
// in step through the triggers, so the cap check reads one row. Entries are
// inserted in seq order and never changed; trimming and eviction delete.
const eventRingSQL = `CREATE TABLE event_ring(
	seq INTEGER PRIMARY KEY CHECK (seq > 0),
	coalesce_key TEXT NOT NULL DEFAULT '',
	created_time INTEGER NOT NULL,
	bytes INTEGER NOT NULL CHECK (bytes > 0),
	event BLOB NOT NULL CHECK (length(event) = bytes)
) STRICT;
CREATE UNIQUE INDEX event_ring_key ON event_ring(coalesce_key) WHERE coalesce_key <> '';
CREATE TABLE event_ring_size(
	id INTEGER PRIMARY KEY CHECK (id = 1),
	events INTEGER NOT NULL CHECK (events >= 0),
	bytes INTEGER NOT NULL CHECK (bytes >= 0)
) STRICT;
INSERT INTO event_ring_size(id, events, bytes) VALUES (1, 0, 0);
CREATE TRIGGER event_ring_in_order BEFORE INSERT ON event_ring
WHEN NEW.seq <= (SELECT max(seq) FROM event_ring)
BEGIN
	SELECT RAISE(ABORT, 'ring entries are inserted in seq order');
END;
CREATE TRIGGER event_ring_kept BEFORE UPDATE ON event_ring
BEGIN
	SELECT RAISE(ABORT, 'a ring entry never changes');
END;
CREATE TRIGGER event_ring_added AFTER INSERT ON event_ring
BEGIN
	UPDATE event_ring_size SET events = events + 1, bytes = bytes + NEW.bytes WHERE id = 1;
END;
CREATE TRIGGER event_ring_removed AFTER DELETE ON event_ring
BEGIN
	UPDATE event_ring_size SET events = events - 1, bytes = bytes - OLD.bytes WHERE id = 1;
END;`

// commandBodySQL adds the encoded Command of each logged entry, so the
// executor can run a received entry, or resume a running one, after a
// restart (02i "What agent.db keeps"; ruling fork 4 iii). An entry logged
// before this migration has none (NULL). Pruning deletes it with its row.
const commandBodySQL = `ALTER TABLE command_log ADD COLUMN command BLOB;`

// rowGuardsSQL closes the paths the UPDATE triggers miss: DELETE (the next
// Open would mint a new install_id), and INSERT OR REPLACE or REPLACE, whose
// implicit delete fires no delete trigger. agent_state is one row, written
// once and never deleted; a command_id is inserted once and never renamed.
// Pruning the command log by age still deletes rows.
const rowGuardsSQL = `CREATE TRIGGER agent_state_never_deleted BEFORE DELETE ON agent_state
BEGIN
	SELECT RAISE(ABORT, 'agent_state is never deleted');
END;
CREATE TRIGGER agent_state_one_row BEFORE INSERT ON agent_state
WHEN EXISTS (SELECT 1 FROM agent_state)
BEGIN
	SELECT RAISE(ABORT, 'agent_state has one row, written once');
END;
CREATE TRIGGER command_log_insert_once BEFORE INSERT ON command_log
WHEN EXISTS (SELECT 1 FROM command_log WHERE command_id = NEW.command_id)
BEGIN
	SELECT RAISE(ABORT, 'a command_id is logged once');
END;
CREATE TRIGGER command_log_id_kept BEFORE UPDATE OF command_id ON command_log
WHEN NEW.command_id IS NOT OLD.command_id
BEGIN
	SELECT RAISE(ABORT, 'a command_id is logged once');
END;`

// ackGuardsSQL keeps the ResultAck time (02i "Command lifecycle"): only a
// finished entry is acked, whether the ack comes by update or in the insert,
// and the first ack time never changes, so an entry that was acked is never
// re-sent.
const ackGuardsSQL = `CREATE TRIGGER command_log_ack_kept BEFORE UPDATE OF acked_time ON command_log
WHEN (OLD.acked_time IS NOT NULL AND NEW.acked_time IS NOT OLD.acked_time)
	OR (NEW.acked_time IS NOT NULL AND NEW.status IS NOT 'finished')
BEGIN
	SELECT RAISE(ABORT, 'a result ack is kept and needs a finished result');
END;
CREATE TRIGGER command_log_ack_finished BEFORE INSERT ON command_log
WHEN NEW.acked_time IS NOT NULL AND NEW.status IS NOT 'finished'
BEGIN
	SELECT RAISE(ABORT, 'a result ack needs a finished result');
END;`

// agentStateSQL is the one row of sequencing state (02i "Sequence numbers"
// and "Connection epoch"; 03n W2 "Persistence"): install_id, seq_hwm and the
// EventCounters, and the Hello.last_epoch and last_applied_command_seq the
// agent reports. The trigger refuses any step back, whoever writes the file.
const agentStateSQL = `CREATE TABLE agent_state(
	id INTEGER PRIMARY KEY CHECK (id = 1),
	install_id TEXT NOT NULL CHECK (length(install_id) BETWEEN 1 AND 64),
	seq_hwm INTEGER NOT NULL DEFAULT 0 CHECK (seq_hwm >= 0),
	shed_total INTEGER NOT NULL DEFAULT 0 CHECK (shed_total >= 0),
	coalesced_total INTEGER NOT NULL DEFAULT 0 CHECK (coalesced_total >= 0),
	evicted_total INTEGER NOT NULL DEFAULT 0 CHECK (evicted_total >= 0),
	last_epoch INTEGER NOT NULL DEFAULT 0 CHECK (last_epoch >= 0),
	last_applied_command_seq INTEGER NOT NULL DEFAULT 0 CHECK (last_applied_command_seq >= 0),
	created_time INTEGER NOT NULL
) STRICT;
CREATE TRIGGER agent_state_never_back BEFORE UPDATE ON agent_state
WHEN NEW.install_id IS NOT OLD.install_id
	OR NEW.seq_hwm < OLD.seq_hwm
	OR NEW.shed_total < OLD.shed_total
	OR NEW.coalesced_total < OLD.coalesced_total
	OR NEW.evicted_total < OLD.evicted_total
	OR NEW.last_epoch < OLD.last_epoch
	OR (NEW.last_epoch = OLD.last_epoch AND NEW.last_applied_command_seq < OLD.last_applied_command_seq)
BEGIN
	SELECT RAISE(ABORT, 'agent_state never goes backwards');
END;`

// commandLogSQL is the command log (03n W2 "Persistence": status, result,
// step reached; 02i "Command lifecycle"). command_id is the key, so a
// re-issued command finds its entry; a finished entry's result never
// changes, so a duplicate is answered with the result first logged.
// acked_time is set by ResultAck. Prune (defaults.md "Agent command log")
// orders by received_time and ages out by it, but deletes only entries that
// are acked or past not_after_time (hub time), never one the hub may still
// re-send.
const commandLogSQL = `CREATE TABLE command_log(
	command_id TEXT PRIMARY KEY CHECK (command_id <> ''),
	kind TEXT NOT NULL CHECK (kind <> ''),
	epoch INTEGER NOT NULL CHECK (epoch >= 0),
	seq INTEGER NOT NULL CHECK (seq >= 0),
	received_time INTEGER NOT NULL,
	not_after_time INTEGER NOT NULL,
	status TEXT NOT NULL CHECK (status IN ('received', 'running', 'finished')),
	step_reached INTEGER NOT NULL DEFAULT 0 CHECK (step_reached >= 0),
	result BLOB,
	finished_time INTEGER,
	acked_time INTEGER,
	CHECK ((status = 'finished') = (result IS NOT NULL AND finished_time IS NOT NULL))
) STRICT;
CREATE INDEX command_log_received ON command_log(received_time);
CREATE TRIGGER command_log_result_kept BEFORE UPDATE OF status, result, finished_time ON command_log
WHEN OLD.status = 'finished' AND (NEW.status IS NOT 'finished'
	OR NEW.result IS NOT OLD.result OR NEW.finished_time IS NOT OLD.finished_time)
BEGIN
	SELECT RAISE(ABORT, 'a logged command result is kept');
END;`
