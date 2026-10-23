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

// machineAdminSQL is migration 18 (plan row 13a slice 4): the machine
// admin writes stamp the rows they change and keep their idempotency
// record.
//
//   - machines gains update_ms, the last owner write (update, revoke,
//     re-enroll); connection and snapshot traffic leaves it alone, so a
//     read can tell an owner change from hub traffic.
//   - machine_request_log keys the idempotency record of UpdateMachine,
//     RevokeMachine and DeleteMachine: (principal, request_id) to the
//     SHA-256 of the request body (to tell a retry from a conflict) and
//     the stored response (02b "Idempotency": the same key and body
//     replays the original response for 24 h). The unique key backs the
//     check up. The response is stored field by field, like the repo log,
//     so a replay rebuilds the answer without re-reading mutable state:
//     the kind, the machine row and its version and stamp. A delete
//     stores its tombstone.
const machineAdminSQL = `CREATE TABLE machine_request_log(
	principal_id TEXT NOT NULL CHECK (principal_id <> ''),
	request_id TEXT NOT NULL CHECK (request_id <> ''),
	request_hash BLOB NOT NULL CHECK (length(request_hash) = 32),
	create_time INTEGER NOT NULL,
	kind TEXT NOT NULL CHECK (kind <> ''),
	machine_id TEXT NOT NULL CHECK (machine_id <> ''),
	response_name TEXT NOT NULL DEFAULT '',
	response_labels TEXT NOT NULL DEFAULT '[]',
	response_expected_hostname TEXT NOT NULL DEFAULT '',
	response_state TEXT NOT NULL DEFAULT '',
	response_version INTEGER NOT NULL DEFAULT 0,
	response_create_ms INTEGER NOT NULL DEFAULT 0,
	response_update_ms INTEGER NOT NULL DEFAULT 0,
	response_hub_seq INTEGER NOT NULL DEFAULT 0,
	deleted INTEGER NOT NULL DEFAULT 0 CHECK (deleted IN (0, 1)),
	PRIMARY KEY (principal_id, request_id)
) STRICT;
ALTER TABLE machines ADD COLUMN update_ms INTEGER NOT NULL DEFAULT 0;`

// Outcomes of the machine admin writes other than success.
var (
	// ErrMachineNotRevoked: DeleteMachine on a machine that is not
	// REVOKED; revoke it first (MACHINE_NOT_REVOKED). A missing row is
	// [ErrMachineUnknown].
	ErrMachineNotRevoked = errors.New("statedb: the machine is not revoked; revoke it first")
)

// machineIDWindowMs is the 02b idempotency window in Unix milliseconds,
// the unit of machine_request_log.create_time.
const machineIDWindowMs = int64((24 * time.Hour) / time.Millisecond)

// MachineWrite is the idempotency and audit envelope of one machine admin
// write. RequestHash is SHA-256 of the request body, to tell a retry from
// a conflict; Actor with an empty ID is Principal.
type MachineWrite struct {
	Principal   string
	RequestID   string
	RequestHash [32]byte
	Actor       Actor
	Now         time.Time
}

// machineKinds name the write of one machine_request_log row.
const (
	machineKindUpdate = "update"
	machineKindRevoke = "revoke"
	machineKindDelete = "delete"
)

// MachineUpdate is one UpdateMachine: the masked owner fields.
// HasName and HasLabels come from the update mask paths
// (spec.display_name, spec.labels).
type MachineUpdate struct {
	MachineWrite
	MachineID       string
	ExpectedVersion uint64
	Name            string
	Labels          []string
	HasName         bool
	HasLabels       bool
}

// MachineStored is one administered machine row: the spec fields an
// owner write changes, the state, the version and times, and the
// revocation record when the machine is REVOKED (read back from its
// machine.revoke audit row, never cleared).
type MachineStored struct {
	MachineID        string
	Name             string
	ExpectedHostname string
	Labels           []string
	Drained          bool
	State            string
	Version          uint64
	CreateMs         int64
	UpdateMs         int64
	HubSeq           int64
	Revoked          bool
	RevokeMs         int64
	RevokeBy         Actor
	RevokeReason     string
	// Replay marks an idempotent retry answering the stored response;
	// Deleted marks the tombstone of a replayed delete.
	Replay  bool
	Deleted bool
	// Row is the full machines row the job captured in its tx for the
	// Watch publish: the after-image at this job's stamps, not the
	// batch-final row a post-commit re-read would see (#403). Empty on
	// replays, which publish nothing.
	Row MachineRow
}

// machineLogRow is one machine_request_log row.
type machineLogRow struct {
	hash     []byte
	createMs int64
	kind     string
	machine  string
	name     string
	labels   string
	expected string
	state    string
	version  int64
	respCms  int64
	respUms  int64
	hubSeq   int64
	deleted  bool
}

func readMachineLog(ctx context.Context, tx *sql.Tx, principal, requestID string) (machineLogRow, error) {
	var row machineLogRow
	var deleted int64
	err := tx.QueryRowContext(ctx, `SELECT request_hash, create_time, kind, machine_id,
		response_name, response_labels, response_expected_hostname, response_state,
		response_version, response_create_ms, response_update_ms, response_hub_seq, deleted
		FROM machine_request_log WHERE principal_id = ? AND request_id = ?`, principal, requestID).
		Scan(&row.hash, &row.createMs, &row.kind, &row.machine, &row.name, &row.labels, &row.expected,
			&row.state, &row.version, &row.respCms, &row.respUms, &row.hubSeq, &deleted)
	if err != nil {
		return machineLogRow{}, err
	}
	row.deleted = deleted == 1
	return row, nil
}

func machineLogExpired(now time.Time, createMs int64) bool {
	return now.UnixMilli()-createMs >= machineIDWindowMs
}

func pruneMachineRequests(ctx context.Context, tx *sql.Tx, now time.Time) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM machine_request_log WHERE rowid IN (
		SELECT rowid FROM machine_request_log WHERE create_time < ? LIMIT 500)`,
		now.UnixMilli()-machineIDWindowMs)
	if err != nil {
		return fmt.Errorf("statedb: prune machine requests: %w", err)
	}
	return nil
}

// storedFromLogRow rebuilds the stored response of a machine request.
func storedFromLogRow(row machineLogRow) (MachineStored, error) {
	var labels []string
	if row.labels != "" {
		if err := json.Unmarshal([]byte(row.labels), &labels); err != nil {
			return MachineStored{}, fmt.Errorf("statedb: machine log labels: %w", err)
		}
	}
	return MachineStored{
		MachineID: row.machine, Name: row.name, ExpectedHostname: row.expected,
		Labels: labels, State: row.state,
		Version:  uint64(row.version), //nolint:gosec // G115: versions start at 1 and only rise
		CreateMs: row.respCms, UpdateMs: row.respUms, HubSeq: row.hubSeq, Replay: true,
	}, nil
}

// checkMachineReplay returns the stored response when the principal
// already used the request_id for the same request body of the same
// kind on the same machine, or [ErrRequestConflict] when the body, the
// kind or the machine differs. The kind matters because a
// RevokeMachineRequest with an empty reason and a DeleteMachineRequest
// share wire fields 1-3, so the same principal, request id, machine id
// and version hash identically across the two RPCs. A miss, or a row
// past the 24 h window (deleted here, with a bounded sweep of its
// peers), is a nil stored machine and no error. A replayed delete
// answers the same empty outcome.
func checkMachineReplay(ctx context.Context, tx *sql.Tx, w MachineWrite, kind, machineID string) (*MachineStored, error) {
	row, err := readMachineLog(ctx, tx, w.Principal, w.RequestID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("statedb: look up machine request_id: %w", err)
	case machineLogExpired(w.Now, row.createMs):
		if _, err := tx.ExecContext(ctx, `DELETE FROM machine_request_log
			WHERE principal_id = ? AND request_id = ?`, w.Principal, w.RequestID); err != nil {
			return nil, fmt.Errorf("statedb: drop expired machine request: %w", err)
		}
		return nil, nil
	case !bytes.Equal(row.hash, w.RequestHash[:]) || row.machine != machineID || row.kind != kind:
		return nil, ErrRequestConflict
	case row.deleted:
		out := MachineStored{MachineID: machineID, Replay: true, Deleted: true}
		return &out, nil
	}
	out, err := storedFromLogRow(row)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func logMachineRequest(ctx context.Context, tx *sql.Tx, w MachineWrite, kind string,
	resp MachineStored, deleted bool,
) error {
	del := 0
	if deleted {
		del = 1
	}
	labels, err := json.Marshal(resp.Labels)
	if err != nil {
		return fmt.Errorf("statedb: log machine labels: %w", err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO machine_request_log(principal_id, request_id, request_hash,
		create_time, kind, machine_id, response_name, response_labels, response_expected_hostname,
		response_state, response_version, response_create_ms, response_update_ms, response_hub_seq, deleted)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		w.Principal, w.RequestID, w.RequestHash[:], w.Now.UnixMilli(), kind, resp.MachineID,
		resp.Name, string(labels), resp.ExpectedHostname, resp.State,
		resp.Version, resp.CreateMs, resp.UpdateMs, resp.HubSeq, del)
	if err != nil {
		return fmt.Errorf("statedb: log machine request: %w", err)
	}
	// Every insert sweeps a bounded batch of expired rows, so the table
	// cannot grow without bound. Replays add no rows and prune nothing.
	return pruneMachineRequests(ctx, tx, w.Now)
}

// readMachineRow reads one machines row with its labels parsed.
func readMachineRow(ctx context.Context, tx *sql.Tx, machineID string) (MachineStored, error) {
	var (
		got               MachineStored
		labels            string
		drained           int
		version, createMs int64
		updateMs, hubSeq  int64
	)
	err := tx.QueryRowContext(ctx, `SELECT machine_id, name, expected_hostname, labels, drained,
		state, version, created_time, update_ms, hub_seq FROM machines WHERE machine_id = ?`, machineID).
		Scan(&got.MachineID, &got.Name, &got.ExpectedHostname, &labels, &drained,
			&got.State, &version, &createMs, &updateMs, &hubSeq)
	if errors.Is(err, sql.ErrNoRows) {
		return MachineStored{}, ErrMachineUnknown
	}
	if err != nil {
		return MachineStored{}, fmt.Errorf("statedb: read machine: %w", err)
	}
	if err := json.Unmarshal([]byte(labels), &got.Labels); err != nil {
		return MachineStored{}, fmt.Errorf("statedb: machine labels: %w", err)
	}
	got.Drained = drained == 1
	got.Version = uint64(version) //nolint:gosec // G115: versions start at 1 and only rise
	got.CreateMs, got.UpdateMs, got.HubSeq = createMs, updateMs, hubSeq
	return got, nil
}

// readRevocation reads the machine's revocation record back from its
// machine.revoke audit row (the actor, the time and the reason), for a
// machine whose state is REVOKED.
func readRevocation(ctx context.Context, tx *sql.Tx, machineID string) (MachineStored, error) {
	var (
		ms             int64
		byID, byKind   string
		byName, detail string
	)
	err := tx.QueryRowContext(ctx, `SELECT create_time, actor_id, actor_kind, actor_name, detail FROM audit_log
		WHERE target = ? AND action = ? ORDER BY create_time DESC, id DESC LIMIT 1`,
		"machines/"+machineID, ActionMachineRevoke).
		Scan(&ms, &byID, &byKind, &byName, &detail)
	if errors.Is(err, sql.ErrNoRows) {
		return MachineStored{}, fmt.Errorf("statedb: revoked machine %s has no machine.revoke audit row", machineID)
	}
	if err != nil {
		return MachineStored{}, fmt.Errorf("statedb: read machine revocation: %w", err)
	}
	var d struct {
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(detail), &d); err != nil {
		return MachineStored{}, fmt.Errorf("statedb: revoke detail: %w", err)
	}
	return MachineStored{
		Revoked: true, RevokeMs: ms,
		RevokeBy:     Actor{ID: byID, Kind: byKind, Name: byName},
		RevokeReason: d.Reason,
	}, nil
}

// withRevocation fills the revocation record of a REVOKED stored machine.
func withRevocation(ctx context.Context, tx *sql.Tx, got MachineStored) (MachineStored, error) {
	if got.State != MachineRevoked {
		return got, nil
	}
	rev, err := readRevocation(ctx, tx, got.MachineID)
	if err != nil {
		return MachineStored{}, err
	}
	got.Revoked, got.RevokeMs, got.RevokeBy, got.RevokeReason = true, rev.RevokeMs, rev.RevokeBy, rev.RevokeReason
	return got, nil
}

// auditMachine appends the audit row of one machine admin write: target
// machines/<id> with the machine id set, the request_id and the row's
// version in detail.
func auditMachine(ctx context.Context, tx *sql.Tx, action string, stored MachineStored, w MachineWrite, extra map[string]string) error {
	detail := map[string]any{"version": stored.Version}
	for k, v := range extra {
		detail[k] = v
	}
	d, err := auditDetail(detail)
	if err != nil {
		return err
	}
	actor := w.Actor
	if actor.ID == "" {
		actor.ID = w.Principal
	}
	_, err = AppendAudit(ctx, tx, AuditEntry{
		Actor: actor, Action: action, Target: "machines/" + stored.MachineID, MachineID: stored.MachineID,
		RequestID: w.RequestID, Detail: d, Now: w.Now,
	})
	return err
}

// UpdateMachine renames and relabels a machine in one writer transaction:
// a replayed request_id returns the stored machine, a missing row is
// [ErrMachineUnknown], a stale version is [VersionStale]. Otherwise the
// masked fields replace the stored ones, the version rises by 1 and the
// row is stamped, with its machine.update audit row. A REVOKED machine
// stays revoked; only its name and labels change.
func UpdateMachine(ctx context.Context, s *store.Store, in MachineUpdate, onCommit func(MachineStored)) (MachineStored, error) {
	jctx := context.WithoutCancel(ctx) // see insertIdentity
	var out MachineStored
	err := writeOrdered(ctx, s, &out, onCommit, func(tx *sql.Tx) error {
		if replay, err := checkMachineReplay(jctx, tx, in.MachineWrite, machineKindUpdate, in.MachineID); err != nil || replay != nil {
			if err == nil {
				if err := validatedWrite(jctx); err != nil {
					return err
				}
				rev, rerr := withRevocation(jctx, tx, *replay)
				if rerr != nil {
					return rerr
				}
				out = rev
			}
			return err
		}
		got, err := readMachineRow(jctx, tx, in.MachineID)
		if err != nil {
			return err
		}
		if err := checkVersion(got.Version, in.ExpectedVersion, "machine"); err != nil {
			return err
		}
		name, labels := got.Name, got.Labels
		if in.HasName {
			name = in.Name
		}
		if in.HasLabels {
			labels = in.Labels
			if labels == nil {
				labels = []string{}
			}
		}
		list, err := json.Marshal(labels)
		if err != nil {
			return fmt.Errorf("statedb: machine labels: %w", err)
		}
		if err := validatedWrite(jctx); err != nil {
			return err
		}
		st, err := NextStamp(jctx, tx, in.Now)
		if err != nil {
			return err
		}
		nowMs := in.Now.UnixMilli()
		if _, err := tx.ExecContext(jctx, `UPDATE machines SET name = ?, labels = ?,
			version = version + 1, update_ms = ?, observed_time = ?, hub_seq = ? WHERE machine_id = ?`,
			name, string(list), nowMs, st.ObservedTime.UnixMilli(), st.HubSeq, in.MachineID); err != nil {
			return fmt.Errorf("statedb: update machine: %w", err)
		}
		out = MachineStored{
			MachineID: got.MachineID, Name: name, ExpectedHostname: got.ExpectedHostname,
			Labels: labels, Drained: got.Drained, State: got.State, Version: got.Version + 1,
			CreateMs: got.CreateMs, UpdateMs: nowMs, HubSeq: st.HubSeq,
		}
		var err2 error
		if out, err2 = withRevocation(jctx, tx, out); err2 != nil {
			return err2
		}
		// Capture the after-image in-tx for the Watch publish: a
		// post-commit re-read would see the batch-final row when a later
		// job in the same batch overwrote this one (#403).
		if row, rerr := readMachineRowTx(jctx, tx, in.MachineID); rerr != nil {
			return rerr
		} else {
			out.Row = row
		}
		if err := auditMachine(jctx, tx, ActionMachineUpdate, out, in.MachineWrite,
			map[string]string{"display_name": name}); err != nil {
			return err
		}
		return logMachineRequest(jctx, tx, in.MachineWrite, machineKindUpdate, out, false)
	})
	if err != nil {
		return MachineStored{}, err
	}
	return out, nil
}
