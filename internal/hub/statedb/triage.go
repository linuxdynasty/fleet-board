package statedb

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/store"
)

// triageHubSQL is migration 19 (plan row 13a slice 5): the hub-state
// triage writes behind SnoozeTriageItem and DismissTriageItem.
//
//   - triage_items holds one row per hub-state item: the 03d scalar
//     core (id, version, kind, severity, state, fingerprint, title, the
//     machine/session/goal/account refs, the detail arm, the snooze and
//     acting stamps; notified_time is hub-internal, for the v3
//     notifier). version starts at 1 and rises by 1 on every operator
//     write; hub_seq is the Watch order (0 = never stamped). Times are
//     Unix milliseconds, 0 when unset. The raise path of plan row 5
//     fills the source columns and adds the pane, work-item and approval
//     refs in its own migration; operator writes only move state.
//   - triage_request_log keys the idempotency record of the two writes:
//     (principal, request_id) to the SHA-256 of the request body (to tell
//     a retry from a conflict) and the stored response (02b
//     "Idempotency": the same key and body replays the original response
//     for 24 h). The response is the acted item as JSON, so a replay
//     rebuilds the answer without re-reading mutable state. The unique
//     key backs the check up.
const triageHubSQL = `CREATE TABLE triage_items(
	id TEXT PRIMARY KEY CHECK (id <> ''),
	version INTEGER NOT NULL CHECK (version >= 1),
	kind TEXT NOT NULL CHECK (kind <> ''),
	severity TEXT NOT NULL CHECK (severity <> ''),
	state TEXT NOT NULL CHECK (state IN ('OPEN', 'SNOOZED', 'IN_FLIGHT', 'ACTED', 'RETIRED', 'EXPIRED', 'DISMISSED')),
	fingerprint TEXT NOT NULL DEFAULT '',
	title TEXT NOT NULL DEFAULT '',
	machine_id TEXT NOT NULL DEFAULT '',
	session_id TEXT NOT NULL DEFAULT '',
	goal_id TEXT NOT NULL DEFAULT '',
	account_id TEXT NOT NULL DEFAULT '',
	detail_kind TEXT NOT NULL DEFAULT '',
	detail_json TEXT NOT NULL DEFAULT '',
	snoozed_until_ms INTEGER NOT NULL DEFAULT 0,
	expire_ms INTEGER NOT NULL DEFAULT 0,
	create_ms INTEGER NOT NULL,
	update_ms INTEGER NOT NULL,
	acted_by_id TEXT NOT NULL DEFAULT '',
	acted_by_kind TEXT NOT NULL DEFAULT '',
	acted_by_name TEXT NOT NULL DEFAULT '',
	acted_ms INTEGER NOT NULL DEFAULT 0,
	last_operation TEXT NOT NULL DEFAULT '',
	notified_ms INTEGER NOT NULL DEFAULT 0,
	hub_seq INTEGER NOT NULL DEFAULT 0
) STRICT;
CREATE TABLE triage_request_log(
	principal_id TEXT NOT NULL CHECK (principal_id <> ''),
	request_id TEXT NOT NULL CHECK (request_id <> ''),
	request_hash BLOB NOT NULL CHECK (length(request_hash) = 32),
	create_time INTEGER NOT NULL,
	kind TEXT NOT NULL CHECK (kind IN ('snooze', 'dismiss')),
	item_id TEXT NOT NULL CHECK (item_id <> ''),
	response_json TEXT NOT NULL CHECK (response_json <> ''),
	PRIMARY KEY (principal_id, request_id)
) STRICT;`

var (
	// ErrTriageUnknown: no triage item has this id.
	ErrTriageUnknown = errors.New("statedb: unknown triage item")
	// ErrTriageClosed: the item already left OPEN and SNOOZED (in
	// flight, acted, retired, expired or dismissed).
	ErrTriageClosed = errors.New("statedb: the triage item is already closed to operator writes")
	// ErrTriagePastUntil: the snooze names a wake time that already
	// passed. It is checked after the replay lookup, so a retry of a
	// committed snooze still returns the stored item.
	ErrTriagePastUntil = errors.New("statedb: the snooze wake time is not in the future")
	// ErrTriageInvalid: an insert or write with no id, or a snooze with
	// no wake time.
	ErrTriageInvalid = errors.New("statedb: the triage write is invalid")
)

// TriageWrite is the idempotency and audit envelope of one hub-state
// triage write. RequestHash is SHA-256 of the request body, to tell a
// retry from a conflict; Actor with an empty ID is Principal.
type TriageWrite struct {
	Principal   string
	RequestID   string
	RequestHash [32]byte
	Actor       Actor
	Now         time.Time
}

// TriageSeed is one hub-state triage row for tests and the raise path:
// the source columns of the 03d persistence row. Operator writes only
// move state, so the seed lands OPEN at version 1.
type TriageSeed struct {
	ID          string
	Kind        string
	Severity    string
	Title       string
	Fingerprint string
	DetailKind  string
	DetailJSON  string
	MachineID   string
	SessionID   string
	GoalID      string
	AccountID   string
	Expire      time.Time
	Now         time.Time
}

// TriageStored is one triage_items row: the acted item a write returns,
// or the stored answer of an idempotent retry (Replay).
type TriageStored struct {
	ID             string
	Version        uint64
	Kind           string
	Severity       string
	State          string
	Title          string
	Fingerprint    string
	DetailKind     string
	DetailJSON     string
	MachineID      string
	SessionID      string
	GoalID         string
	AccountID      string
	SnoozedUntilMs int64
	ExpireMs       int64
	CreateMs       int64
	UpdateMs       int64
	ActedBy        Actor
	ActedMs        int64
	LastOperation  string
	HubSeq         int64
	Replay         bool
}

// TriageSnooze is one snooze: the item's wake time. ExpectedVersion 0
// skips the check (02b "Versions and ETags"); the RPC requires it.
type TriageSnooze struct {
	Write           TriageWrite
	ID              string
	ExpectedVersion uint64
	Until           time.Time
}

// TriageDismiss is one dismiss: the item closes without acting, with the
// operator's note kept in the audit row.
type TriageDismiss struct {
	Write           TriageWrite
	ID              string
	ExpectedVersion uint64
	Note            string
}

// InsertTriageItem inserts one hub-state triage row OPEN at version 1,
// stamped for Watch. It writes no audit row: the raise path of plan row
// 5 audits the raise with its source event.
func InsertTriageItem(ctx context.Context, s *store.Store, seed TriageSeed) (TriageStored, error) {
	if seed.ID == "" || seed.Kind == "" || seed.Severity == "" {
		return TriageStored{}, fmt.Errorf("%w: a triage item needs an id, a kind and a severity", ErrTriageInvalid)
	}
	if seed.Now.IsZero() {
		return TriageStored{}, ErrZeroTime
	}
	if seed.DetailKind != "" && !json.Valid([]byte(seed.DetailJSON)) {
		return TriageStored{}, fmt.Errorf("%w: the triage detail is not valid JSON", ErrTriageInvalid)
	}
	jctx := context.WithoutCancel(ctx) // see insertIdentity
	var out TriageStored
	err := s.Write(ctx, func(tx *sql.Tx) error {
		st, err := NextStamp(jctx, tx, seed.Now)
		if err != nil {
			return err
		}
		nowMs := seed.Now.UnixMilli()
		if _, err := tx.ExecContext(jctx, `INSERT INTO triage_items(id, version, kind, severity, state,
			fingerprint, title, machine_id, session_id, goal_id, account_id,
			detail_kind, detail_json, expire_ms, create_ms, update_ms, hub_seq)
			VALUES (?, 1, ?, ?, 'OPEN', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			seed.ID, seed.Kind, seed.Severity, seed.Fingerprint, seed.Title,
			seed.MachineID, seed.SessionID, seed.GoalID, seed.AccountID,
			seed.DetailKind, seed.DetailJSON, seed.Expire.UnixMilli(), nowMs, nowMs, st.HubSeq); err != nil {
			return fmt.Errorf("statedb: insert triage item: %w", err)
		}
		out = TriageStored{
			ID: seed.ID, Version: 1, Kind: seed.Kind, Severity: seed.Severity,
			State: "OPEN", Title: seed.Title, Fingerprint: seed.Fingerprint,
			DetailKind: seed.DetailKind, DetailJSON: seed.DetailJSON,
			MachineID: seed.MachineID, SessionID: seed.SessionID,
			GoalID: seed.GoalID, AccountID: seed.AccountID,
			ExpireMs: seed.Expire.UnixMilli(), CreateMs: nowMs, UpdateMs: nowMs,
			HubSeq: st.HubSeq,
		}
		return nil
	})
	if err != nil {
		return TriageStored{}, err
	}
	return out, nil
}

// triageReplay returns the stored answer of an idempotent retry: same key
// and body replays the acted item, another body is [ErrRequestConflict].
// A row past the 24 h window is dropped and its key is free again.
func triageReplay(ctx context.Context, tx *sql.Tx, w TriageWrite, kind string) (*TriageStored, error) {
	var hash []byte
	var createMs int64
	var response string
	err := tx.QueryRowContext(ctx, `SELECT request_hash, create_time, response_json FROM triage_request_log
		WHERE principal_id = ? AND request_id = ?`, w.Principal, w.RequestID).Scan(&hash, &createMs, &response)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("statedb: look up triage request_id: %w", err)
	case w.Now.UnixMilli()-createMs > configIDWindowMs:
		if _, err := tx.ExecContext(ctx, `DELETE FROM triage_request_log
			WHERE principal_id = ? AND request_id = ?`, w.Principal, w.RequestID); err != nil {
			return nil, fmt.Errorf("statedb: drop expired triage request: %w", err)
		}
		return nil, nil
	case !bytes.Equal(hash, w.RequestHash[:]):
		return nil, ErrRequestConflict
	}
	var out TriageStored
	if err := json.Unmarshal([]byte(response), &out); err != nil {
		return nil, fmt.Errorf("statedb: decode stored triage response: %w", err)
	}
	out.Replay = true
	return &out, nil
}

// pruneTriageRequests deletes expired idempotency rows, at most a bounded
// batch per writer job, so the table cannot grow without bound.
func pruneTriageRequests(ctx context.Context, tx *sql.Tx, now time.Time) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM triage_request_log WHERE rowid IN (
		SELECT rowid FROM triage_request_log WHERE create_time < ? LIMIT 500)`,
		now.UnixMilli()-configIDWindowMs)
	if err != nil {
		return fmt.Errorf("statedb: prune triage requests: %w", err)
	}
	return nil
}

// logTriageRequest stores the acted item as the idempotency record of one
// triage write. The unique key on (principal, request_id) backs the
// replay check up.
func logTriageRequest(ctx context.Context, tx *sql.Tx, w TriageWrite, kind string, out TriageStored) error {
	raw, err := json.Marshal(out)
	if err != nil {
		return fmt.Errorf("statedb: encode triage response: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO triage_request_log(principal_id, request_id,
		request_hash, create_time, kind, item_id, response_json)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		w.Principal, w.RequestID, w.RequestHash[:], w.Now.UnixMilli(), kind, out.ID, string(raw)); err != nil {
		return fmt.Errorf("statedb: log triage request: %w", err)
	}
	return nil
}

// readTriageItem reads one triage_items row ([ErrTriageUnknown] when
// missing) for an operator write.
func readTriageItem(ctx context.Context, tx *sql.Tx, id string) (TriageStored, error) {
	var out TriageStored
	var version uint64
	var hubSeq int64
	err := tx.QueryRowContext(ctx, `SELECT id, version, kind, severity, state, fingerprint, title,
		machine_id, session_id, goal_id, account_id, detail_kind, detail_json,
		snoozed_until_ms, expire_ms, create_ms, update_ms,
		acted_by_id, acted_by_kind, acted_by_name, acted_ms, last_operation, hub_seq
		FROM triage_items WHERE id = ?`, id).Scan(
		&out.ID, &version, &out.Kind, &out.Severity, &out.State, &out.Fingerprint, &out.Title,
		&out.MachineID, &out.SessionID, &out.GoalID, &out.AccountID, &out.DetailKind, &out.DetailJSON,
		&out.SnoozedUntilMs, &out.ExpireMs, &out.CreateMs, &out.UpdateMs,
		&out.ActedBy.ID, &out.ActedBy.Kind, &out.ActedBy.Name, &out.ActedMs, &out.LastOperation, &hubSeq)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return TriageStored{}, ErrTriageUnknown
	case err != nil:
		return TriageStored{}, fmt.Errorf("statedb: read triage item: %w", err)
	}
	out.Version = version
	out.HubSeq = hubSeq
	return out, nil
}

// checkTriageOpen refuses a write against an item that already left OPEN
// and SNOOZED. The closed state wins over a stale version (02b
// precedence, like APPROVAL_CLOSED): the phone says "already handled",
// not "reload".
func checkTriageOpen(state string) error {
	if state != "OPEN" && state != "SNOOZED" {
		return ErrTriageClosed
	}
	return nil
}

// auditTriage appends the audit row of one triage write in its
// transaction, so both commit or neither does. Target is triage/<id>.
func auditTriage(ctx context.Context, tx *sql.Tx, action, id string, w TriageWrite, detail string) error {
	if _, err := AppendAudit(ctx, tx, AuditEntry{
		Actor: w.Actor, Action: action, Target: "triage/" + id,
		RequestID: w.RequestID, Detail: detail, Now: w.Now,
	}); err != nil {
		return fmt.Errorf("statedb: append %s audit row: %w", action, err)
	}
	return nil
}

// SnoozeTriageItem snoozes an OPEN or SNOOZED item in one writer
// transaction. In order: a replayed request_id returns the stored item
// ([ErrRequestConflict] on another body); an unknown id is
// [ErrTriageUnknown]; an item past OPEN and SNOOZED is [ErrTriageClosed]
// before any version check; a stale expected_version is a [VersionStale]
// naming the triage version; a wake time at or before now is
// [ErrTriagePastUntil]. Otherwise the row moves to SNOOZED at
// version + 1, stamped, with its triage.snooze audit row, and a bounded
// batch of expired idempotency rows is swept. Nothing is written on any
// error.
func SnoozeTriageItem(ctx context.Context, s *store.Store, in TriageSnooze) (TriageStored, error) {
	if in.ID == "" {
		return TriageStored{}, fmt.Errorf("%w: a snooze needs an item id", ErrTriageInvalid)
	}
	jctx := context.WithoutCancel(ctx) // see insertIdentity
	var out TriageStored
	err := s.Write(ctx, func(tx *sql.Tx) error {
		if replay, err := triageReplay(jctx, tx, in.Write, "snooze"); err != nil || replay != nil {
			if err == nil {
				out = *replay
			}
			return err
		}
		got, err := readTriageItem(jctx, tx, in.ID)
		if err != nil {
			return err
		}
		if err := checkTriageOpen(got.State); err != nil {
			return err
		}
		if err := checkVersion(got.Version, in.ExpectedVersion, "triage"); err != nil {
			return err
		}
		if !in.Until.After(in.Write.Now) {
			return ErrTriagePastUntil
		}
		if err := pruneTriageRequests(jctx, tx, in.Write.Now); err != nil {
			return err
		}
		st, err := NextStamp(jctx, tx, in.Write.Now)
		if err != nil {
			return err
		}
		nowMs, untilMs := in.Write.Now.UnixMilli(), in.Until.UnixMilli()
		if _, err := tx.ExecContext(jctx, `UPDATE triage_items SET state = 'SNOOZED',
			version = version + 1, snoozed_until_ms = ?, update_ms = ?, hub_seq = ? WHERE id = ?`,
			untilMs, nowMs, st.HubSeq, in.ID); err != nil {
			return fmt.Errorf("statedb: snooze triage item: %w", err)
		}
		detail, err := auditDetail(struct {
			RequestID string `json:"request_id"`
			Item      string `json:"item"`
			UntilTime string `json:"until_time"`
			Version   uint64 `json:"version"`
		}{in.Write.RequestID, in.ID, in.Until.UTC().Format(time.RFC3339), got.Version + 1})
		if err != nil {
			return err
		}
		if err := auditTriage(jctx, tx, ActionTriageSnooze, in.ID, in.Write, detail); err != nil {
			return err
		}
		out = got
		out.State = "SNOOZED"
		out.Version = got.Version + 1
		out.SnoozedUntilMs = untilMs
		out.UpdateMs = nowMs
		out.HubSeq = st.HubSeq
		return logTriageRequest(jctx, tx, in.Write, "snooze", out)
	})
	if err != nil {
		return TriageStored{}, err
	}
	return out, nil
}

// DismissTriageItem closes an OPEN or SNOOZED item without acting, in one
// writer transaction. The order of checks matches
// [SnoozeTriageItem]: replay, unknown id, closed state, stale version.
// Otherwise the row moves to DISMISSED at version + 1 with the operator
// and time recorded, stamped, with its triage.dismiss audit row carrying
// the note. Nothing is written on any error.
func DismissTriageItem(ctx context.Context, s *store.Store, in TriageDismiss) (TriageStored, error) {
	if in.ID == "" {
		return TriageStored{}, fmt.Errorf("%w: a dismiss needs an item id", ErrTriageInvalid)
	}
	jctx := context.WithoutCancel(ctx) // see insertIdentity
	var out TriageStored
	err := s.Write(ctx, func(tx *sql.Tx) error {
		if replay, err := triageReplay(jctx, tx, in.Write, "dismiss"); err != nil || replay != nil {
			if err == nil {
				out = *replay
			}
			return err
		}
		got, err := readTriageItem(jctx, tx, in.ID)
		if err != nil {
			return err
		}
		if err := checkTriageOpen(got.State); err != nil {
			return err
		}
		if err := checkVersion(got.Version, in.ExpectedVersion, "triage"); err != nil {
			return err
		}
		if err := pruneTriageRequests(jctx, tx, in.Write.Now); err != nil {
			return err
		}
		st, err := NextStamp(jctx, tx, in.Write.Now)
		if err != nil {
			return err
		}
		nowMs := in.Write.Now.UnixMilli()
		if _, err := tx.ExecContext(jctx, `UPDATE triage_items SET state = 'DISMISSED',
			version = version + 1, snoozed_until_ms = 0,
			acted_by_id = ?, acted_by_kind = ?, acted_by_name = ?,
			acted_ms = ?, update_ms = ?, hub_seq = ? WHERE id = ?`,
			in.Write.Actor.ID, in.Write.Actor.Kind, in.Write.Actor.Name,
			nowMs, nowMs, st.HubSeq, in.ID); err != nil {
			return fmt.Errorf("statedb: dismiss triage item: %w", err)
		}
		detail, err := auditDetail(struct {
			RequestID string `json:"request_id"`
			Item      string `json:"item"`
			Note      string `json:"note"`
			Version   uint64 `json:"version"`
		}{in.Write.RequestID, in.ID, in.Note, got.Version + 1})
		if err != nil {
			return err
		}
		if err := auditTriage(jctx, tx, ActionTriageDismiss, in.ID, in.Write, detail); err != nil {
			return err
		}
		out = got
		out.State = "DISMISSED"
		out.Version = got.Version + 1
		out.SnoozedUntilMs = 0
		out.ActedBy = in.Write.Actor
		out.ActedMs = nowMs
		out.UpdateMs = nowMs
		out.HubSeq = st.HubSeq
		return logTriageRequest(jctx, tx, in.Write, "dismiss", out)
	})
	if err != nil {
		return TriageStored{}, err
	}
	return out, nil
}
