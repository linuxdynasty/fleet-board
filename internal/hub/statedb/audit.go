package statedb

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/store"
)

// auditSQL is migration 6 (plan row 5, PR 5d): the permanent audit log, the
// hub-wide Watch sequence, and the Watch stamp on machines.
//
//   - hub_counters holds one row (id = 1): hub_seq, the last Watch sequence
//     handed out ([NextStamp]). It only rises; a job that fails rolls its
//     numbers back with its savepoint.
//   - audit_log is AuditEntry (hub/v1/settings.proto) with ids from
//     AUTOINCREMENT, so an id is never reused. Triggers refuse UPDATE and
//     DELETE: rows are never changed or pruned (03f).
//   - machines gains version (spec; 1 at insert), observed_time and hub_seq
//     (status; 0 = never stamped).
const auditSQL = `CREATE TABLE hub_counters(
	id INTEGER PRIMARY KEY CHECK (id = 1),
	hub_seq INTEGER NOT NULL CHECK (hub_seq >= 0)
) STRICT;
INSERT INTO hub_counters(id, hub_seq) VALUES (1, 0);
CREATE TABLE audit_log(
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	create_time INTEGER NOT NULL,
	hub_seq INTEGER NOT NULL UNIQUE CHECK (hub_seq > 0),
	actor_id TEXT NOT NULL DEFAULT '',
	actor_kind TEXT NOT NULL DEFAULT '',
	actor_name TEXT NOT NULL DEFAULT '',
	action TEXT NOT NULL CHECK (action <> ''),
	target TEXT NOT NULL CHECK (target <> ''),
	machine_id TEXT NOT NULL DEFAULT '',
	goal_id TEXT NOT NULL DEFAULT '',
	command_id TEXT NOT NULL DEFAULT '',
	request_id TEXT NOT NULL DEFAULT '',
	outcome TEXT NOT NULL DEFAULT 'OK' CHECK (outcome <> ''),
	reason TEXT NOT NULL DEFAULT '',
	detail TEXT NOT NULL DEFAULT '{}' CHECK (length(CAST(detail AS BLOB)) <= 1024),
	occurred_time INTEGER
) STRICT;
CREATE INDEX audit_log_time ON audit_log(create_time, id);
CREATE INDEX audit_log_target ON audit_log(target, create_time);
CREATE INDEX audit_log_machine ON audit_log(machine_id, create_time) WHERE machine_id <> '';
CREATE TRIGGER audit_log_no_update BEFORE UPDATE ON audit_log
BEGIN SELECT RAISE(ABORT, 'audit_log rows are permanent'); END;
CREATE TRIGGER audit_log_no_delete BEFORE DELETE ON audit_log
BEGIN SELECT RAISE(ABORT, 'audit_log rows are permanent'); END;
ALTER TABLE machines ADD COLUMN version INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1);
ALTER TABLE machines ADD COLUMN observed_time INTEGER NOT NULL DEFAULT 0;
ALTER TABLE machines ADD COLUMN hub_seq INTEGER NOT NULL DEFAULT 0 CHECK (hub_seq >= 0);`

// Audit actions written by the hub so far (02m "Audit actions").
const (
	ActionEnrollToken  = "machine.enroll_token"
	ActionEnroll       = "machine.enroll"
	ActionEnrollFailed = "machine.enroll_failed"
	ActionCertRenewed  = "machine.cert_renewed"
)

// MaxAuditDetail caps AuditEntry.detail in bytes; the table checks it too.
const MaxAuditDetail = 1024

// Actor is AuditEntry.actor, a Principal: its id (the filter key), its kind
// without the PRINCIPAL_KIND_ prefix, and its display name. The zero value
// is no actor: a step nobody asked for, such as an agent's Enroll.
type Actor struct {
	ID, Kind, Name string
}

// AuditEntry is one audit_log row to append. Outcome is the Status name
// without STATUS_ (empty means OK) and Reason the ReasonCode name without
// REASON_CODE_. Detail is a short JSON object with no secret in it. Occurred
// is set only when the thing happened at another time than Now (the hub
// clock at the job, the row's create_time).
type AuditEntry struct {
	Actor     Actor
	Action    string
	Target    string
	MachineID string
	GoalID    string
	CommandID string
	RequestID string
	Outcome   string
	Reason    string
	Detail    string
	Occurred  time.Time
	Now       time.Time
}

// Stamp is the Watch stamp of one changed resource: its hub_seq and
// observed_time, the hub's clock in the job that changed it (never an
// agent's clock).
type Stamp struct {
	HubSeq       int64
	ObservedTime time.Time
}

// NextStamp takes the next hub_seq inside the writer job's transaction tx.
// Call it once per changed resource. When the job fails, its savepoint
// rolls the number back with the change, so committed numbers rise in
// commit order (02c "Order"). A version is never bumped here: version
// covers the spec only and is 1 at insert (02b, ADR 0019).
func NextStamp(ctx context.Context, tx *sql.Tx, now time.Time) (Stamp, error) {
	var seq int64
	err := tx.QueryRowContext(ctx, `UPDATE hub_counters SET hub_seq = hub_seq + 1 WHERE id = 1 RETURNING hub_seq`).Scan(&seq)
	if err != nil {
		return Stamp{}, fmt.Errorf("statedb: next hub_seq: %w", err)
	}
	return Stamp{HubSeq: seq, ObservedTime: now}, nil
}

// AppendAudit stamps e and inserts it in tx, the transaction of the change
// it records, so both commit or neither does. It returns the row id.
func AppendAudit(ctx context.Context, tx *sql.Tx, e AuditEntry) (int64, error) {
	switch {
	case e.Action == "" || e.Target == "":
		return 0, errors.New("statedb: an audit entry needs an action and a target")
	case len(e.Detail) > MaxAuditDetail:
		return 0, fmt.Errorf("statedb: audit detail is %d bytes, over %d", len(e.Detail), MaxAuditDetail)
	}
	if e.Outcome == "" {
		e.Outcome = "OK"
	}
	if e.Detail == "" {
		e.Detail = "{}"
	}
	var occurred sql.NullInt64
	if !e.Occurred.IsZero() {
		occurred = sql.NullInt64{Int64: e.Occurred.UnixMilli(), Valid: true}
	}
	st, err := NextStamp(ctx, tx, e.Now)
	if err != nil {
		return 0, err
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO audit_log(create_time, hub_seq, actor_id, actor_kind, actor_name, action,
		target, machine_id, goal_id, command_id, request_id, outcome, reason, detail, occurred_time)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.Now.UnixMilli(), st.HubSeq, e.Actor.ID, e.Actor.Kind, e.Actor.Name, e.Action, e.Target, e.MachineID, e.GoalID,
		e.CommandID, e.RequestID, e.Outcome, e.Reason, e.Detail, occurred)
	if err != nil {
		return 0, fmt.Errorf("statedb: append %s audit row: %w", e.Action, err)
	}
	return res.LastInsertId()
}

// WriteAudit appends entries in one writer job of their own, for a step that
// changes nothing else (a refused Enroll). All are written or none.
func WriteAudit(ctx context.Context, s *store.Store, entries ...AuditEntry) error {
	jctx := context.WithoutCancel(ctx) // see insertIdentity
	return s.Write(ctx, func(tx *sql.Tx) error {
		for _, e := range entries {
			if _, err := AppendAudit(jctx, tx, e); err != nil {
				return err
			}
		}
		return nil
	})
}

// auditDetail is v as a compact JSON object for AuditEntry.detail.
func auditDetail(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("statedb: audit detail: %w", err)
	}
	return string(b), nil
}
