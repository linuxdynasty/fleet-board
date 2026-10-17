package statedb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/linuxdynasty/fleet-board/internal/store"
)

// accountsTableSQL holds one row per harness account the owner created
// (account.proto): the natural key (harness, display_name) the export and
// the import match on, the AccountSpec as protojson, the origin (always
// owner-created in this slice; discovery arrives later), the version (1
// at insert, +1 per spec change) and hub_seq (Watch order). Status is
// observed and computed, never stored.
const accountsTableSQL = `CREATE TABLE accounts(
	id TEXT PRIMARY KEY CHECK (id <> ''),
	harness TEXT NOT NULL CHECK (harness <> ''),
	provider TEXT NOT NULL CHECK (provider <> ''),
	display_name TEXT NOT NULL CHECK (display_name <> ''),
	origin TEXT NOT NULL CHECK (origin <> ''),
	spec_json TEXT NOT NULL DEFAULT '{}',
	version INTEGER NOT NULL CHECK (version >= 1),
	create_ms INTEGER NOT NULL,
	update_ms INTEGER NOT NULL,
	hub_seq INTEGER NOT NULL DEFAULT 0,
	UNIQUE (harness, display_name)
) STRICT;`

// Account origins (account.proto AccountOrigin).
const (
	// AccountOriginOwner is an API-key account the owner created.
	AccountOriginOwner = "OWNER_CREATED"
)

// Outcomes of the owner account writes other than success (02b
// "Idempotency", "Versions and ETags").
var (
	// ErrAccountExists: the harness already has an account under this
	// display name (ALREADY_EXISTS).
	ErrAccountExists = errors.New("statedb: this account already exists")
	// ErrAccountUnknown: no account has this id (NOT_FOUND).
	ErrAccountUnknown = errors.New("statedb: unknown account")
)

// CreateAccount is one owner-created API-key account.
type CreateAccount struct {
	ConfigWrite
	ID          string
	Harness     string
	Provider    string
	DisplayName string
	Origin      string
	// Spec is the AccountSpec as protojson, stored whole.
	Spec []byte
}

// UpdateAccountOp is a masked spec rewrite of an account at
// ExpectedVersion; 0 skips the check (02b "Versions and ETags").
type UpdateAccountOp struct {
	ConfigWrite
	ID              string
	ExpectedVersion uint64
	// Merge computes the new spec and display name from the stored
	// protojson. It runs inside the writer transaction, so two updates
	// to different fields keep each other's changes. An error aborts
	// without writing.
	Merge func(current []byte) (merged []byte, displayName string, err error)
}

// DeleteAccountOp removes an account at ExpectedVersion; 0 skips the
// check. Nothing may use it: no table references an account yet (no
// dispatch rows, no discovery), so the ACCOUNT_IN_USE refusal of 02m
// follows with them.
type DeleteAccountOp struct {
	ConfigWrite
	ID              string
	ExpectedVersion uint64
}

// AccountStored is the account after the write. Replay is set when the
// principal already used the request_id for the same request: the stored
// response comes back and nothing is written twice.
type AccountStored struct {
	ID          string
	Harness     string
	Provider    string
	DisplayName string
	Origin      string
	Version     uint64
	CreateMs    int64
	UpdateMs    int64
	HubSeq      int64
	Spec        []byte
	Replay      bool
}

func accountTarget(id string) string { return "accounts/" + id }

func auditAccount(ctx context.Context, tx *sql.Tx, action, id string, version uint64, w ConfigWrite) error {
	detail, err := auditDetail(struct {
		ID      string `json:"id"`
		Version uint64 `json:"version"`
	}{id, version})
	if err != nil {
		return err
	}
	_, err = AppendAudit(ctx, tx, AuditEntry{
		Actor: configActor(w), Action: action, Target: accountTarget(id),
		RequestID: w.RequestID, Detail: detail, Now: w.Now,
	})
	return err
}

func readAccount(ctx context.Context, tx *sql.Tx, id string) (AccountStored, error) {
	var got AccountStored
	var spec string
	var version, createMs, updateMs, hubSeq int64
	err := tx.QueryRowContext(ctx, `SELECT id, harness, provider, display_name, origin,
		spec_json, version, create_ms, update_ms, hub_seq FROM accounts WHERE id = ?`, id).
		Scan(&got.ID, &got.Harness, &got.Provider, &got.DisplayName, &got.Origin,
			&spec, &version, &createMs, &updateMs, &hubSeq)
	if errors.Is(err, sql.ErrNoRows) {
		return AccountStored{}, ErrAccountUnknown
	}
	if err != nil {
		return AccountStored{}, fmt.Errorf("statedb: read account: %w", err)
	}
	got.Spec = []byte(spec)
	got.Version = uint64(version) //nolint:gosec // G115: versions start at 1 and only rise
	got.CreateMs, got.UpdateMs, got.HubSeq = createMs, updateMs, hubSeq
	return got, nil
}

// GetAccount reads one account (ErrAccountUnknown when missing).
func GetAccount(ctx context.Context, s *store.Store, id string) (AccountStored, error) {
	var out AccountStored
	err := s.Read(ctx, func(tx *sql.Tx) error {
		var err error
		out, err = readAccount(ctx, tx, id)
		return err
	})
	if err != nil {
		return AccountStored{}, err
	}
	return out, nil
}

// AccountRow is one account for the export and the import diff, in
// harness/display-name order.
type AccountRow struct {
	Harness     string
	Provider    string
	DisplayName string
	Spec        []byte
}

// ListAccountRows reads every account in harness/display-name order, so
// the export renders a stable document and the import diffs against one.
func ListAccountRows(ctx context.Context, s *store.Store) ([]AccountRow, error) {
	var out []AccountRow
	err := s.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT harness, provider, display_name, spec_json
			FROM accounts ORDER BY harness, display_name`)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var r AccountRow
			var spec string
			if err := rows.Scan(&r.Harness, &r.Provider, &r.DisplayName, &spec); err != nil {
				return err
			}
			r.Spec = []byte(spec)
			out = append(out, r)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("statedb: list accounts: %w", err)
	}
	return out, nil
}

// ListAccountIDs reads every account id in id order, so subject enumeration
// (05b GetSystemStatus) renders a stable list.
func ListAccountIDs(ctx context.Context, s *store.Store) ([]string, error) {
	var out []string
	err := s.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT id FROM accounts ORDER BY id`)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			out = append(out, id)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("statedb: list account ids: %w", err)
	}
	return out, nil
}

// CreateOwnerAccount inserts an owner-created account in one writer
// transaction. In order: a replayed request_id returns the stored
// account ([ErrRequestConflict] on another body or resource); an
// existing (harness, display_name) is [ErrAccountExists]; otherwise the
// row lands at version 1, stamped, with its account.create audit row.
// Nothing is written on any error. The unique key on (principal,
// request_id) backs the replay check up.
func CreateOwnerAccount(ctx context.Context, s *store.Store, in CreateAccount) (AccountStored, error) {
	jctx := context.WithoutCancel(ctx) // see insertIdentity
	var out AccountStored
	err := s.Write(ctx, func(tx *sql.Tx) error {
		if row, err := configReplay(jctx, tx, in.ConfigWrite, configKindAccount); err != nil || row != nil {
			if err == nil {
				out = replayAccount(row)
			}
			return err
		}
		var exists bool
		err := tx.QueryRowContext(jctx, `SELECT TRUE FROM accounts WHERE harness = ? AND display_name = ?`,
			in.Harness, in.DisplayName).Scan(&exists)
		switch {
		case err == nil:
			return ErrAccountExists
		case !errors.Is(err, sql.ErrNoRows):
			return fmt.Errorf("statedb: look up account: %w", err)
		}
		st, err := NextStamp(jctx, tx, in.Now)
		if err != nil {
			return err
		}
		nowMs := in.Now.UnixMilli()
		if _, err := tx.ExecContext(jctx, `INSERT INTO accounts(id, harness, provider, display_name,
			origin, spec_json, version, create_ms, update_ms, hub_seq)
			VALUES (?, ?, ?, ?, ?, ?, 1, ?, ?, ?)`,
			in.ID, in.Harness, in.Provider, in.DisplayName, in.Origin,
			string(in.Spec), nowMs, nowMs, st.HubSeq); err != nil {
			return fmt.Errorf("statedb: insert account: %w", err)
		}
		if err := auditAccount(jctx, tx, ActionAccountCreate, in.ID, 1, in.ConfigWrite); err != nil {
			return err
		}
		if err := logConfigRequest(jctx, tx, in.ConfigWrite, configKindAccount, in.ID,
			in.Harness, in.Provider, in.Origin, string(in.Spec), 1, nowMs, nowMs, st.HubSeq, "", false); err != nil {
			return err
		}
		out = AccountStored{
			ID: in.ID, Harness: in.Harness, Provider: in.Provider,
			DisplayName: in.DisplayName, Origin: in.Origin,
			Version: 1, CreateMs: nowMs, UpdateMs: nowMs, HubSeq: st.HubSeq, Spec: in.Spec,
		}
		return nil
	})
	if err != nil {
		return AccountStored{}, err
	}
	return out, nil
}

// replayAccount rebuilds the stored account of an idempotent retry: the
// fields of the original call, marked as a replay so nothing is written
// twice.
func replayAccount(row *configLogRow) AccountStored {
	return AccountStored{
		ID: row.resourceID, Harness: row.harness, Provider: row.provider,
		Origin: row.origin, Version: uint64(row.version), //nolint:gosec // G115: versions start at 1 and only rise
		CreateMs: row.respCms, UpdateMs: row.respUms, HubSeq: row.hubSeq,
		Spec: []byte(row.spec), Replay: true,
	}
}

// UpdateOwnerAccount rewrites an account's masked spec in one writer
// transaction: a replayed request_id returns the stored account, a
// missing row is [ErrAccountUnknown], a stale version is [VersionStale].
// Otherwise the merged spec replaces the stored one, the version rises
// by 1 and the row is stamped, with its account.update audit row.
func UpdateOwnerAccount(ctx context.Context, s *store.Store, in UpdateAccountOp) (AccountStored, error) {
	jctx := context.WithoutCancel(ctx) // see insertIdentity
	var out AccountStored
	err := s.Write(ctx, func(tx *sql.Tx) error {
		if row, err := configReplay(jctx, tx, in.ConfigWrite, configKindAccount); err != nil || row != nil {
			if err == nil {
				out = replayAccount(row)
			}
			return err
		}
		got, err := readAccount(jctx, tx, in.ID)
		if err != nil {
			return err
		}
		if err := checkVersion(got.Version, in.ExpectedVersion, "account"); err != nil {
			return err
		}
		merged, displayName, err := in.Merge(got.Spec)
		if err != nil {
			return err
		}
		if displayName != got.DisplayName {
			var clash bool
			err := tx.QueryRowContext(jctx, `SELECT TRUE FROM accounts
				WHERE harness = ? AND display_name = ? AND id <> ?`,
				got.Harness, displayName, in.ID).Scan(&clash)
			switch {
			case err == nil:
				return ErrAccountExists
			case !errors.Is(err, sql.ErrNoRows):
				return fmt.Errorf("statedb: look up account: %w", err)
			}
		}
		st, err := NextStamp(jctx, tx, in.Now)
		if err != nil {
			return err
		}
		nowMs := in.Now.UnixMilli()
		if _, err := tx.ExecContext(jctx, `UPDATE accounts SET display_name = ?, spec_json = ?,
			version = version + 1, update_ms = ?, hub_seq = ? WHERE id = ?`,
			displayName, string(merged), nowMs, st.HubSeq, in.ID); err != nil {
			return fmt.Errorf("statedb: update account: %w", err)
		}
		if err := auditAccount(jctx, tx, ActionAccountUpdate, in.ID, got.Version+1, in.ConfigWrite); err != nil {
			return err
		}
		if err := logConfigRequest(jctx, tx, in.ConfigWrite, configKindAccount, in.ID,
			got.Harness, got.Provider, got.Origin, string(merged), int64(got.Version)+1, //nolint:gosec // G115: versions start at 1 and only rise
			got.CreateMs, nowMs, st.HubSeq, "", false); err != nil {
			return err
		}
		out = AccountStored{
			ID: in.ID, Harness: got.Harness, Provider: got.Provider,
			DisplayName: displayName, Origin: got.Origin,
			Version: got.Version + 1, CreateMs: got.CreateMs, UpdateMs: nowMs,
			HubSeq: st.HubSeq, Spec: merged,
		}
		return nil
	})
	if err != nil {
		return AccountStored{}, err
	}
	return out, nil
}

// DeleteOwnerAccount removes an account in one writer transaction: a
// replayed request_id answers the same empty outcome, a missing row is
// [ErrAccountUnknown], a stale version is [VersionStale]. Otherwise the
// row is deleted with its account.delete audit row.
func DeleteOwnerAccount(ctx context.Context, s *store.Store, in DeleteAccountOp) (AccountStored, error) {
	jctx := context.WithoutCancel(ctx) // see insertIdentity
	var out AccountStored
	err := s.Write(ctx, func(tx *sql.Tx) error {
		if row, err := configReplay(jctx, tx, in.ConfigWrite, configKindAccount); err != nil || row != nil {
			if err == nil {
				out = AccountStored{
					ID:      in.ID,
					Version: uint64(row.version), //nolint:gosec // G115: versions start at 1 and only rise
					Replay:  true,
				}
			}
			return err
		}
		got, err := readAccount(jctx, tx, in.ID)
		if err != nil {
			return err
		}
		if err := checkVersion(got.Version, in.ExpectedVersion, "account"); err != nil {
			return err
		}
		if _, err := tx.ExecContext(jctx, `DELETE FROM accounts WHERE id = ?`, in.ID); err != nil {
			return fmt.Errorf("statedb: delete account: %w", err)
		}
		if err := auditAccount(jctx, tx, ActionAccountDelete, in.ID, got.Version, in.ConfigWrite); err != nil {
			return err
		}
		if err := logConfigRequest(jctx, tx, in.ConfigWrite, configKindAccount, in.ID,
			got.Harness, got.Provider, got.Origin, "", int64(got.Version), //nolint:gosec // G115: versions start at 1 and only rise
			got.CreateMs, got.UpdateMs, got.HubSeq, "", true); err != nil {
			return err
		}
		out = AccountStored{ID: in.ID, Version: got.Version}
		return nil
	})
	if err != nil {
		return AccountStored{}, err
	}
	return out, nil
}
