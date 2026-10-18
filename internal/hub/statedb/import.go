package statedb

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/store"
)

// importRequestLogSQL is migration 16 (plan row 13a): the config import
// keeps its idempotency record. import_request_log keys (principal,
// request_id) to the SHA-256 of the request body (to tell a retry from a
// conflict) and the stored ImportConfigResponse (02b "Idempotency": the same
// key and body replays the original response for 24 h). The unique key backs
// the check up.
const importRequestLogSQL = `CREATE TABLE import_request_log(
	principal_id TEXT NOT NULL CHECK (principal_id <> ''),
	request_id TEXT NOT NULL CHECK (request_id <> ''),
	request_hash BLOB NOT NULL CHECK (length(request_hash) = 32),
	create_time INTEGER NOT NULL,
	response BLOB NOT NULL,
	PRIMARY KEY (principal_id, request_id)
) STRICT;`

// importIDWindowMs is the 02b idempotency window in Unix milliseconds, the
// unit of import_request_log.create_time: a (principal, request_id) replays
// its stored response for 24 h, then the key is free again and expired rows
// are pruned.
const importIDWindowMs = int64((24 * time.Hour) / time.Millisecond)

// ImportWrite is the idempotency and audit envelope of one config import.
// RequestHash is SHA-256 of the request body, to tell a retry from a
// conflict; Actor with an empty ID is Principal.
type ImportWrite struct {
	Principal   string
	RequestID   string
	RequestHash [32]byte
	Actor       Actor
	Now         time.Time
}

// ImportRepoOp is one repo write of a verified import plan. Create inserts
// the repo at version 1; an update replaces the spec and bumps the version
// when it still holds ExpectedVersion, so a repo rewritten after the dry
// run aborts the whole apply instead of losing the concurrent change.
type ImportRepoOp struct {
	Host     string
	Path     string
	CodeHost string
	Enabled  bool
	// Spec is the RepoSpec as protojson, stored whole.
	Spec   []byte
	Create bool
	// ExpectedVersion is the repo's version at plan time (updates only).
	ExpectedVersion uint64
}

// ErrImportStale is a repo or machine rewritten after the import plan:
// the apply aborts with nothing written, and the caller runs a dry run
// again.
var ErrImportStale = errors.New("statedb: the repo changed since the import plan")

// ImportMachineOp is one enrolled machine's write of a verified import
// plan: the entry's labels replace the machine's (nil leaves them) and
// the entry's values replace the machine's settings layer (nil leaves
// it; import never deletes what the document does not name). The drain
// flag is never touched: import drains no enrolled machine. DropPending
// deletes a kept row shadowing the name (only a rename can leave one)
// in the same transaction.
type ImportMachineOp struct {
	MachineID string
	// Name is the display name the plan matched: the apply verifies the
	// machine still holds it, so a rename since the dry run aborts the
	// whole apply instead of labelling the wrong row.
	Name string
	// ExpectedVersion is the machine's version at plan time: the apply
	// verifies it still holds, so an owner write since the dry run
	// (UpdateMachine, revoke, re-enroll) aborts the whole apply instead
	// of losing the concurrent change. Heartbeats never bump the
	// version, so they never trip it.
	ExpectedVersion uint64
	Labels          []string
	// Layer is the SettingsSpec as protojson, nil to leave the layer.
	Layer       []byte
	DropPending string
}

// ImportPendingOp is one machine entry of a verified import plan kept
// for a name no machine has: the labels with the settings layer's
// values (a SettingsSpec as protojson with the scope unset), applied
// when a machine enrolls under the name.
type ImportPendingOp struct {
	Name   string
	Labels []string
	// Layer is the SettingsSpec as protojson with the scope unset.
	Layer []byte
}

// ConfigImport applies a verified import plan in one writer
// transaction: every repo, machine and pending op lands and the
// config.import audit row with it, or nothing does. PlanHash, Created,
// Updated, Machines and Pending go in the audit detail; Response is
// the deterministic ImportConfigResponse bytes kept for idempotent replay.
type ConfigImport struct {
	ImportWrite
	PlanHash string
	Created  int
	Updated  int
	Machines int
	Pending  int
	Ops      []ImportRepoOp
	// MachineOps relabels enrolled machines and replaces their layers.
	MachineOps []ImportMachineOp
	// PendingOps keeps entries for names no machine has.
	PendingOps []ImportPendingOp
	Response   []byte
}

// RepoSpecRow is one watched repo for import diffing, in host/path order.
type RepoSpecRow struct {
	Host    string
	Path    string
	Version uint64
	Spec    []byte
}

// ListAllWatchedRepoSpecs reads every watched repo's spec and version in
// host/path order, so an import plan diffs against a stable snapshot and
// the apply can verify nothing moved under it.
func ListAllWatchedRepoSpecs(ctx context.Context, s *store.Store) ([]RepoSpecRow, error) {
	var out []RepoSpecRow
	err := s.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT host, path, version, spec_json FROM repos ORDER BY host, path`)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var r RepoSpecRow
			var spec string
			var version int64
			if err := rows.Scan(&r.Host, &r.Path, &version, &spec); err != nil {
				return err
			}
			r.Version = uint64(version) //nolint:gosec // G115: versions start at 1 and only rise
			r.Spec = []byte(spec)
			out = append(out, r)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("statedb: list watched repo specs: %w", err)
	}
	return out, nil
}

func readImportLog(ctx context.Context, tx *sql.Tx, principal, requestID string) (hash []byte, createMs int64, response []byte, err error) {
	err = tx.QueryRowContext(ctx, `SELECT request_hash, create_time, response FROM import_request_log
		WHERE principal_id = ? AND request_id = ?`, principal, requestID).
		Scan(&hash, &createMs, &response)
	return hash, createMs, response, err
}

func importLogExpired(now time.Time, createMs int64) bool {
	return now.UnixMilli()-createMs >= importIDWindowMs
}

// LookupImportReplay is the read-pool pre-check behind an import apply:
// the stored response of a (principal, request_id) retry,
// [ErrRequestConflict] for another body, or a miss (nil, nil) when the key
// is new or its row is past the 24 h window. It never deletes; the writer
// transaction owns the expiry. A caller that only wants an early replay
// keeps validation order by acting on a hit and a conflict before the
// plan-hash check 02b ranks after ALREADY_EXISTS.
func LookupImportReplay(ctx context.Context, s *store.Store, principal, requestID string,
	hash [32]byte, now time.Time,
) ([]byte, error) {
	var out []byte
	err := s.Read(ctx, func(tx *sql.Tx) error {
		storedHash, createMs, response, err := readImportLog(ctx, tx, principal, requestID)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return nil
		case err != nil:
			return fmt.Errorf("statedb: look up import request_id: %w", err)
		case importLogExpired(now, createMs):
			return nil
		case !bytes.Equal(storedHash, hash[:]):
			return ErrRequestConflict
		}
		out = response
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// pruneImportRequests deletes expired idempotency rows, at most a bounded
// batch per writer job, so the table cannot grow without bound.
func pruneImportRequests(ctx context.Context, tx *sql.Tx, now time.Time) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM import_request_log WHERE rowid IN (
		SELECT rowid FROM import_request_log WHERE create_time < ? LIMIT 500)`,
		now.UnixMilli()-importIDWindowMs)
	if err != nil {
		return fmt.Errorf("statedb: prune import requests: %w", err)
	}
	return nil
}

func insertImportRepo(ctx context.Context, tx *sql.Tx, now time.Time, st Stamp, op ImportRepoOp) error {
	enabled := 0
	if op.Enabled {
		enabled = 1
	}
	nowMs := now.UnixMilli()
	if op.Create {
		var exists bool
		err := tx.QueryRowContext(ctx, `SELECT TRUE FROM repos WHERE host = ? AND path = ?`,
			op.Host, op.Path).Scan(&exists)
		switch {
		case err == nil:
			return ErrRepoExists
		case !errors.Is(err, sql.ErrNoRows):
			return fmt.Errorf("statedb: look up repo: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO repos(host, path, code_host, enabled,
			spec_json, version, create_ms, update_ms, hub_seq) VALUES (?, ?, ?, ?, ?, 1, ?, ?, ?)`,
			op.Host, op.Path, op.CodeHost, enabled, string(op.Spec), nowMs, nowMs, st.HubSeq); err != nil {
			return fmt.Errorf("statedb: insert repo: %w", err)
		}
		return nil
	}
	var current uint64
	err := tx.QueryRowContext(ctx, `SELECT version FROM repos WHERE host = ? AND path = ?`,
		op.Host, op.Path).Scan(&current)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return ErrRepoUnknown
	case err != nil:
		return fmt.Errorf("statedb: read repo version: %w", err)
	case current != op.ExpectedVersion:
		return ErrImportStale
	}
	if _, err := tx.ExecContext(ctx, `UPDATE repos SET code_host = ?, enabled = ?, spec_json = ?,
		version = version + 1, update_ms = ?, hub_seq = ? WHERE host = ? AND path = ?`,
		op.CodeHost, enabled, string(op.Spec), nowMs, st.HubSeq, op.Host, op.Path); err != nil {
		return fmt.Errorf("statedb: update repo: %w", err)
	}
	return nil
}

// validateImportPlan repeats the dry run's mutable checks in the writer
// transaction before quota admission or any import effect. The apply still
// checks each row while writing, but no other writer can interleave here.
func validateImportPlan(ctx context.Context, tx *sql.Tx, in ConfigImport) error {
	seenRepos := make(map[string]bool, len(in.Ops))
	for _, op := range in.Ops {
		key := op.Host + "\x00" + op.Path
		if seenRepos[key] {
			if op.Create {
				return ErrRepoExists
			}
			return ErrImportStale
		}
		seenRepos[key] = true
		var version uint64
		err := tx.QueryRowContext(ctx, `SELECT version FROM repos WHERE host = ? AND path = ?`,
			op.Host, op.Path).Scan(&version)
		if op.Create {
			switch {
			case err == nil:
				return ErrRepoExists
			case !errors.Is(err, sql.ErrNoRows):
				return fmt.Errorf("statedb: look up repo: %w", err)
			}
			continue
		}
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return ErrRepoUnknown
		case err != nil:
			return fmt.Errorf("statedb: read repo version: %w", err)
		case version != op.ExpectedVersion:
			return ErrImportStale
		}
	}
	seenMachines := make(map[string]bool, len(in.MachineOps))
	for _, op := range in.MachineOps {
		if seenMachines[op.MachineID] {
			return ErrImportStale
		}
		seenMachines[op.MachineID] = true
		var version uint64
		err := tx.QueryRowContext(ctx, `SELECT version FROM machines
			WHERE machine_id = ? AND name = ?`, op.MachineID, op.Name).Scan(&version)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return ErrImportStale
		case err != nil:
			return fmt.Errorf("statedb: match import machine: %w", err)
		case version != op.ExpectedVersion:
			return ErrImportStale
		}
		if op.Labels != nil {
			if _, err := marshalLabels(op.Labels); err != nil {
				return err
			}
		}
	}
	for _, op := range in.PendingOps {
		var taken bool
		err := tx.QueryRowContext(ctx, `SELECT TRUE FROM machines WHERE name = ?`, op.Name).Scan(&taken)
		switch {
		case errors.Is(err, sql.ErrNoRows):
		case err != nil:
			return fmt.Errorf("statedb: match pending machine: %w", err)
		default:
			return ErrImportStale
		}
		if _, err := marshalLabels(op.Labels); err != nil {
			return err
		}
	}
	return nil
}

// ApplyConfigImport applies a verified import plan in one writer
// transaction. In order: a replayed request_id returns the stored response
// ([ErrRequestConflict] on another body); otherwise every repo, machine
// and pending op is applied (a repo created since the dry run is
// [ErrRepoExists], one deleted since is [ErrRepoUnknown]; a machine
// renamed, deleted or rewritten since is [ErrImportStale]), stamped, with
// one config.import
// audit row (target config) and the idempotency record. Nothing is
// written on any error. The unique key on (principal, request_id) backs
// the replay check up. A row past the 24 h window is dropped here, with
// a bounded sweep of its peers, and its key is free again.
func ApplyConfigImport(ctx context.Context, s *store.Store, in ConfigImport) (resp []byte, replay bool, err error) {
	jctx := context.WithoutCancel(ctx) // see insertIdentity
	err = s.Write(ctx, func(tx *sql.Tx) error {
		hash, createMs, stored, rerr := readImportLog(jctx, tx, in.Principal, in.RequestID)
		switch {
		case errors.Is(rerr, sql.ErrNoRows):
			// New key: fall through to the apply.
		case rerr != nil:
			return fmt.Errorf("statedb: look up import request_id: %w", rerr)
		case importLogExpired(in.Now, createMs):
			if _, err := tx.ExecContext(jctx, `DELETE FROM import_request_log
				WHERE principal_id = ? AND request_id = ?`, in.Principal, in.RequestID); err != nil {
				return fmt.Errorf("statedb: drop expired import request: %w", err)
			}
		case !bytes.Equal(hash, in.RequestHash[:]):
			return ErrRequestConflict
		default:
			if err := validatedWrite(jctx); err != nil {
				return err
			}
			resp, replay = stored, true
			return nil
		}
		if err := validateImportPlan(jctx, tx, in); err != nil {
			return err
		}
		if err := validatedWrite(jctx); err != nil {
			return err
		}
		for _, op := range in.Ops {
			st, err := NextStamp(jctx, tx, in.Now)
			if err != nil {
				return err
			}
			if err := insertImportRepo(jctx, tx, in.Now, st, op); err != nil {
				return err
			}
		}
		for _, op := range in.MachineOps {
			st, err := NextStamp(jctx, tx, in.Now)
			if err != nil {
				return err
			}
			var version int64
			if err := tx.QueryRowContext(jctx, `SELECT version FROM machines
				WHERE machine_id = ? AND name = ?`, op.MachineID, op.Name).Scan(&version); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return ErrImportStale
				}
				return fmt.Errorf("statedb: match import machine: %w", err)
			}
			if uint64(version) != op.ExpectedVersion { //nolint:gosec // G115: versions start at 1 and only rise
				return ErrImportStale
			}
			if op.Labels != nil {
				if err := applyMachineLabelsTx(jctx, tx, op.MachineID, op.Labels, in.Now, st); err != nil {
					return err
				}
			}
			if op.Layer != nil {
				if err := upsertMachineLayerTx(jctx, tx, op.MachineID, op.Layer, in.Now, st); err != nil {
					return err
				}
			}
			if op.DropPending != "" {
				if _, err := tx.ExecContext(jctx, `DELETE FROM pending_machines WHERE name = ?`,
					op.DropPending); err != nil {
					return fmt.Errorf("statedb: drop shadowed pending machine: %w", err)
				}
			}
		}
		for _, op := range in.PendingOps {
			st, err := NextStamp(jctx, tx, in.Now)
			if err != nil {
				return err
			}
			// A machine enrolled under the name since the dry run
			// owns it now: the apply aborts like a rewritten repo
			// instead of shadowing the enrolled row.
			var taken bool
			merr := tx.QueryRowContext(jctx, `SELECT TRUE FROM machines WHERE name = ?`,
				op.Name).Scan(&taken)
			switch {
			case errors.Is(merr, sql.ErrNoRows):
				// The name is still free.
			case merr != nil:
				return fmt.Errorf("statedb: match pending machine: %w", merr)
			default:
				return ErrImportStale
			}
			labels, err := marshalLabels(op.Labels)
			if err != nil {
				return err
			}
			if err := upsertPendingTx(jctx, tx, op.Name, labels, string(op.Layer), in.Now, st); err != nil {
				return err
			}
		}
		detail, err := auditDetail(struct {
			PlanHash string `json:"plan_hash"`
			Created  int    `json:"created"`
			Updated  int    `json:"updated"`
			Machines int    `json:"machines"`
			Pending  int    `json:"pending"`
		}{in.PlanHash, in.Created, in.Updated, in.Machines, in.Pending})
		if err != nil {
			return err
		}
		actor := in.Actor
		if actor.ID == "" {
			actor.ID = in.Principal
		}
		if _, err := AppendAudit(jctx, tx, AuditEntry{
			Actor: actor, Action: ActionConfigImport, Target: "config",
			RequestID: in.RequestID, Detail: detail, Now: in.Now,
		}); err != nil {
			return err
		}
		if _, err := tx.ExecContext(jctx, `INSERT INTO import_request_log(principal_id, request_id,
			request_hash, create_time, response) VALUES (?, ?, ?, ?, ?)`,
			in.Principal, in.RequestID, in.RequestHash[:], in.Now.UnixMilli(), in.Response); err != nil {
			return fmt.Errorf("statedb: log import request: %w", err)
		}
		if err := pruneImportRequests(jctx, tx, in.Now); err != nil {
			return err
		}
		resp = in.Response
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return resp, replay, nil
}
