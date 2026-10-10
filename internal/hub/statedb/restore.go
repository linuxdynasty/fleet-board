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

// restoreSQL adds the end of the reconcile window fleetd restore opens, in
// Unix milliseconds like created_time; 0 is no window (02g, 03f).
const restoreSQL = `ALTER TABLE hub_identity ADD COLUMN restore_reconcile_until INTEGER NOT NULL DEFAULT 0;`

// RestoreUpdate is what fleetd restore changes in the hub_identity row of a
// restored store. Everything else (fleet id, CA, salt) stays.
type RestoreUpdate struct {
	// SettingsEpoch is the new settings incarnation; it must differ from
	// the restored one so agents take the hub's settings again.
	SettingsEpoch string
	// HubAddresses replaces the stored list; nil stores an empty list.
	HubAddresses []string
	// ServerCert, ServerKey and ServerNames replace the server certificate
	// when ServerCert is set (fleetd restore --address).
	ServerCert, ServerKey []byte
	ServerNames           []string
	// ReconcileUntil ends the 24 h reconcile window.
	ReconcileUntil time.Time
	// Audit, when set, is appended (stamped with a hub_seq) in the same
	// transaction: the hub.restore row of 02m.
	Audit *AuditEntry
}

// ApplyRestore writes u to the hub_identity row, and its audit row, in one
// writer transaction: every field and the row are written, or none is.
func ApplyRestore(ctx context.Context, s *store.Store, u RestoreUpdate) error {
	addrs := u.HubAddresses
	if addrs == nil {
		addrs = []string{}
	}
	hub, err := json.Marshal(addrs)
	if err != nil {
		return fmt.Errorf("statedb: hub addresses: %w", err)
	}
	names, err := json.Marshal(u.ServerNames)
	if err != nil {
		return fmt.Errorf("statedb: server names: %w", err)
	}
	jctx := context.WithoutCancel(ctx) // see insertIdentity
	return s.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(jctx, `UPDATE hub_identity SET settings_epoch = ?, hub_addresses = ?,
			restore_reconcile_until = ? WHERE id = 1`, u.SettingsEpoch, string(hub), u.ReconcileUntil.UnixMilli())
		if err != nil {
			return fmt.Errorf("statedb: restore hub_identity: %w", err)
		}
		if n, err := res.RowsAffected(); err != nil || n != 1 {
			return errors.Join(ErrNoIdentity, err)
		}
		if u.ServerCert != nil {
			_, err = tx.ExecContext(jctx, `UPDATE hub_identity SET server_cert = ?, server_key = ?, server_names = ?
				WHERE id = 1`, u.ServerCert, u.ServerKey, string(names))
			if err != nil {
				return fmt.Errorf("statedb: restore server certificate: %w", err)
			}
		}
		if u.Audit != nil {
			if _, err := AppendAudit(jctx, tx, *u.Audit); err != nil {
				return err
			}
		}
		return nil
	})
}
