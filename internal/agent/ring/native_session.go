package ring

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

const nativeSessionColumns = `session_id, machine_id, install_id, harness, interface,
	account_ref, profile_ref, workdir_ref, model, effort, qualification_ref,
	controller_id, generation, control_state, provider_session_id, start_effect_id`

func readNativeSession(ctx context.Context, tx *sql.Tx, id string) (NativeSessionRecord, error) {
	var r NativeSessionRecord
	var generation uint64
	var provider, startEffect sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT `+nativeSessionColumns+` FROM native_sessions WHERE session_id = ?`, id).
		Scan(&r.SessionID, &r.MachineID, &r.InstallID, &r.Harness, &r.Interface,
			&r.AccountRef, &r.ProfileRef, &r.WorkdirRef, &r.Model, &r.Effort,
			&r.QualificationRef, &r.ControllerID, &generation, &r.ControlState, &provider, &startEffect)
	if errors.Is(err, sql.ErrNoRows) {
		return NativeSessionRecord{}, ErrNativeUnknownSession
	}
	if err != nil {
		return NativeSessionRecord{}, fmt.Errorf("ring: read native session: %w", err)
	}
	r.Generation = generation
	r.ProviderSessionID = provider.String
	r.StartEffectID = startEffect.String
	return r, nil
}

// ReadNativeSession reads durable machine-side ownership by the existing
// Fleet SessionId. It does not create a second public session identity.
func (d *DB) ReadNativeSession(ctx context.Context, id string) (NativeSessionRecord, error) {
	var out NativeSessionRecord
	err := d.s.Read(ctx, func(tx *sql.Tx) error {
		var err error
		out, err = readNativeSession(ctx, tx, id)
		return err
	})
	return out, err
}

func nativeSessionSame(a, b NativeSessionRecord) bool {
	a.ProviderSessionID, b.ProviderSessionID = "", ""
	a.StartEffectID, b.StartEffectID = "", ""
	a.ControlState, b.ControlState = "", ""
	return a == b
}

// CreateNativeSession records the initial managed controller. A repeated
// identical request returns the existing row. The installation must match
// this agent.db; ownership cannot be minted by reconnecting.
func (d *DB) CreateNativeSession(ctx context.Context, r NativeSessionRecord) (NativeSessionRecord, error) {
	if err := r.validate(); err != nil {
		return NativeSessionRecord{}, err
	}
	var out NativeSessionRecord
	err := d.nativeWrite(ctx, func(tx *sql.Tx) error {
		stored, err := readNativeSession(ctx, tx, r.SessionID)
		if err == nil {
			if !nativeSessionSame(stored, r) ||
				(r.ProviderSessionID != "" && r.ProviderSessionID != stored.ProviderSessionID) ||
				(r.StartEffectID != "" && r.StartEffectID != stored.StartEffectID) {
				return ErrNativeConflict
			}
			out = stored
			return nil
		}
		if !errors.Is(err, ErrNativeUnknownSession) {
			return err
		}
		var installID string
		if err := tx.QueryRowContext(ctx, `SELECT install_id FROM agent_state WHERE id = 1`).Scan(&installID); err != nil {
			return fmt.Errorf("ring: read native install binding: %w", err)
		}
		if installID != r.InstallID {
			return ErrNativeOwner
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO native_sessions(`+nativeSessionColumns+`)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			r.SessionID, r.MachineID, r.InstallID, r.Harness, r.Interface,
			r.AccountRef, r.ProfileRef, r.WorkdirRef, r.Model, r.Effort,
			r.QualificationRef, r.ControllerID, r.Generation, r.ControlState, nil, nil)
		if err != nil {
			return fmt.Errorf("ring: create native session: %w", errors.Join(ErrNativeConflict, err))
		}
		out = r
		return nil
	})
	if err != nil {
		return NativeSessionRecord{}, err
	}
	return out, nil
}

// BindNativeProviderSession records the provider identity with the exact
// claimed start effect once. This is durable correlation, not proof that the
// provider supplied that identity; a qualified adapter must prove that later.
func (d *DB) BindNativeProviderSession(ctx context.Context, id, controller string, generation uint64, startEffectID, providerID string) (NativeSessionRecord, error) {
	if !validNativeRef(startEffectID, 128) || !validNativeRef(providerID, 256) {
		return NativeSessionRecord{}, ErrNativeBadInput
	}
	var out NativeSessionRecord
	err := d.nativeWrite(ctx, func(tx *sql.Tx) error {
		r, err := readNativeSession(ctx, tx, id)
		if err != nil {
			return err
		}
		if r.ControllerID != controller || r.Generation != generation {
			return ErrNativeOwner
		}
		if r.ProviderSessionID != "" {
			if r.ProviderSessionID != providerID || r.StartEffectID != startEffectID {
				return ErrNativeConflict
			}
			out = r
			return nil
		}
		start, err := readNativeEffect(ctx, tx, startEffectID)
		if err != nil || start.SessionID != id || start.Generation != generation ||
			start.Kind != NativeEffectStart ||
			(start.State != NativeEffectMayHaveRun && start.State != NativeEffectReconcileRequired &&
				start.State != NativeEffectConfirmed) {
			return ErrNativeConflict
		}
		if _, err := tx.ExecContext(ctx, `UPDATE native_sessions SET provider_session_id = ?, start_effect_id = ?
			WHERE session_id = ? AND provider_session_id IS NULL`, providerID, startEffectID, id); err != nil {
			return fmt.Errorf("ring: bind native provider: %w", errors.Join(ErrNativeConflict, err))
		}
		out, err = readNativeSession(ctx, tx, id)
		return err
	})
	if err != nil {
		return NativeSessionRecord{}, err
	}
	return out, nil
}
