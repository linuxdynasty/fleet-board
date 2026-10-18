package ring

import (
	"context"
	"database/sql"
	"fmt"
)

// NativeEvidence names a closed source class and SHA-256 of its bounded
// non-secret receipt. The receipt itself is never saved in agent.db.
type NativeEvidence struct {
	Kind, Digest string
}

const (
	NativeEvidenceProviderReceipt = "provider_receipt"
	NativeEvidenceProviderAbsence = "provider_absence"
	NativeEvidenceOperator        = "operator_reconciled"
	NativeEvidencePrelaunch       = "prelaunch_refusal"
)

func (e NativeEvidence) validFor(outcome NativeEffectState) bool {
	if !validDigest(e.Digest) {
		return false
	}
	switch outcome {
	case NativeEffectConfirmed:
		return e.Kind == NativeEvidenceProviderReceipt || e.Kind == NativeEvidenceOperator
	case NativeEffectNoEffect:
		return e.Kind == NativeEvidenceProviderAbsence || e.Kind == NativeEvidenceOperator || e.Kind == NativeEvidencePrelaunch
	default:
		return false
	}
}

// ResolveNativeEffect records a bounded proof of one external effect. A
// confirmed input says nothing about turn end, process exit, cleanup or goal
// verification. A PREPARED effect can only resolve to NO_EFFECT.
func (d *DB) ResolveNativeEffect(ctx context.Context, id, controller string, generation uint64, outcome NativeEffectState, evidence NativeEvidence) (NativeEffectRecord, error) {
	if !validNativeRef(id, 128) || !validNativeRef(controller, 128) || generation == 0 || !evidence.validFor(outcome) {
		return NativeEffectRecord{}, ErrNativeBadInput
	}
	var out NativeEffectRecord
	err := d.s.Write(ctx, func(tx *sql.Tx) error {
		e, err := readNativeEffect(ctx, tx, id)
		if err != nil {
			return err
		}
		s, err := readNativeSession(ctx, tx, e.SessionID)
		if err != nil {
			return err
		}
		if s.ControllerID != controller || s.Generation != generation || e.Generation != generation {
			return ErrNativeOwner
		}
		if e.State == outcome && e.EvidenceKind == evidence.Kind && e.EvidenceDigest == evidence.Digest {
			out = e
			return nil
		}
		if e.State == NativeEffectConfirmed || e.State == NativeEffectNoEffect ||
			(e.State == NativeEffectPrepared && outcome != NativeEffectNoEffect) {
			return ErrNativeConflict
		}
		if outcome == NativeEffectNoEffect {
			var bound bool
			if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM native_sessions
				WHERE start_effect_id = ?)`, id).Scan(&bound); err != nil {
				return fmt.Errorf("ring: check native start binding: %w", err)
			}
			if bound {
				return ErrNativeConflict
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE native_effects
			SET state = ?, evidence_kind = ?, evidence_digest = ? WHERE effect_id = ?`,
			outcome, evidence.Kind, evidence.Digest, id); err != nil {
			return fmt.Errorf("ring: resolve native effect: %w", err)
		}
		e.State, e.EvidenceKind, e.EvidenceDigest = outcome, evidence.Kind, evidence.Digest
		out = e
		return nil
	})
	if err != nil {
		return NativeEffectRecord{}, err
	}
	return out, nil
}

// RecoverNativeEffects inspects one bounded page after a restart. Every
// MAY_HAVE_RUN on the page becomes RECONCILE_REQUIRED, and its session is
// held in the same writer job. PREPARED grants no external-call authority and
// remains prepared. Use the last returned EffectID as the next cursor.
func (d *DB) RecoverNativeEffects(ctx context.Context, afterEffectID string, limit int) ([]NativeEffectRecord, error) {
	if err := nativePageLimit(limit); err != nil {
		return nil, err
	}
	if afterEffectID != "" && !validNativeRef(afterEffectID, 128) {
		return nil, ErrNativeBadInput
	}
	var out []NativeEffectRecord
	err := d.s.Write(ctx, func(tx *sql.Tx) error {
		out = nil
		ids, err := nativeUnresolvedIDs(ctx, tx, afterEffectID, limit)
		if err != nil {
			return err
		}
		for _, id := range ids {
			e, err := readNativeEffect(ctx, tx, id)
			if err != nil {
				return err
			}
			if e.State == NativeEffectMayHaveRun {
				if _, err := tx.ExecContext(ctx, `UPDATE native_effects SET state = 'reconcile_required'
					WHERE effect_id = ?`, id); err != nil {
					return fmt.Errorf("ring: recover native effect: %w", err)
				}
				if _, err := tx.ExecContext(ctx, `UPDATE native_sessions SET control_state = 'held'
					WHERE session_id = ? AND control_state = 'managed'`, e.SessionID); err != nil {
					return fmt.Errorf("ring: hold native session: %w", err)
				}
				e.State = NativeEffectReconcileRequired
			}
			out = append(out, e)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func nativeUnresolvedIDs(ctx context.Context, tx *sql.Tx, after string, limit int) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT effect_id FROM native_effects
		WHERE effect_id > ? AND state IN ('prepared', 'may_have_run', 'reconcile_required')
		ORDER BY effect_id LIMIT ?`, after, limit)
	if err != nil {
		return nil, fmt.Errorf("ring: list unresolved native effects: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("ring: list unresolved native effects: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("ring: list unresolved native effects: %w", err)
	}
	return ids, nil
}

// ListUnresolvedNativeEffects reads a bounded page without changing state.
// The cursor is the previous page's last EffectID; an empty cursor starts at
// the beginning. This is for recovery and operator inspection, not polling.
func (d *DB) ListUnresolvedNativeEffects(ctx context.Context, afterEffectID string, limit int) ([]NativeEffectRecord, error) {
	if err := nativePageLimit(limit); err != nil {
		return nil, err
	}
	if afterEffectID != "" && !validNativeRef(afterEffectID, 128) {
		return nil, ErrNativeBadInput
	}
	var out []NativeEffectRecord
	err := d.s.Read(ctx, func(tx *sql.Tx) error {
		ids, err := nativeUnresolvedIDs(ctx, tx, afterEffectID, limit)
		if err != nil {
			return err
		}
		for _, id := range ids {
			e, err := readNativeEffect(ctx, tx, id)
			if err != nil {
				return err
			}
			out = append(out, e)
		}
		return nil
	})
	return out, err
}
