package ring

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
)

const nativeEffectColumns = `effect_id, command_id, session_id, generation, kind,
	command_body_digest, admission_order, state, retry_of_effect_id, evidence_kind, evidence_digest`

func readNativeEffect(ctx context.Context, tx *sql.Tx, id string) (NativeEffectRecord, error) {
	var r NativeEffectRecord
	var generation, order uint64
	err := tx.QueryRowContext(ctx, `SELECT `+nativeEffectColumns+` FROM native_effects WHERE effect_id = ?`, id).
		Scan(&r.EffectID, &r.CommandID, &r.SessionID, &generation, &r.Kind,
			&r.CommandBodyDigest, &order, &r.State, &r.RetryOfEffectID, &r.EvidenceKind, &r.EvidenceDigest)
	if errors.Is(err, sql.ErrNoRows) {
		return NativeEffectRecord{}, ErrNativeUnknownEffect
	}
	if err != nil {
		return NativeEffectRecord{}, fmt.Errorf("ring: read native effect: %w", err)
	}
	r.Generation, r.AdmissionOrder = generation, order
	return r, nil
}

// ReadNativeEffect reads one retained intent after an uncertain write or a
// lost response. Reading it never grants permission to call the provider.
func (d *DB) ReadNativeEffect(ctx context.Context, id string) (NativeEffectRecord, error) {
	var out NativeEffectRecord
	err := d.s.Read(ctx, func(tx *sql.Tx) error {
		var err error
		out, err = readNativeEffect(ctx, tx, id)
		return err
	})
	return out, err
}

func nativeEffectSame(a, b NativeEffectRecord) bool {
	a.AdmissionOrder, b.AdmissionOrder = 0, 0
	a.State, b.State = "", ""
	a.EvidenceKind, b.EvidenceKind = "", ""
	a.EvidenceDigest, b.EvidenceDigest = "", ""
	return a == b
}

// AdmitNativeEffect binds an input to a logged command and saves PREPARED.
// It grants no external-call authority. The digest must match the saved
// command body, so a caller cannot change the input under a stable ID.
func (d *DB) AdmitNativeEffect(ctx context.Context, r NativeEffectRecord) (NativeEffectRecord, bool, error) {
	if err := r.validate(); err != nil {
		return NativeEffectRecord{}, false, err
	}
	var out NativeEffectRecord
	var duplicate bool
	err := d.s.Write(ctx, func(tx *sql.Tx) error {
		stored, err := readNativeEffect(ctx, tx, r.EffectID)
		if err == nil {
			if !nativeEffectSame(stored, r) {
				return ErrNativeConflict
			}
			out, duplicate = stored, true
			return nil
		}
		if !errors.Is(err, ErrNativeUnknownEffect) {
			return err
		}
		var commandOwner string
		err = tx.QueryRowContext(ctx, `SELECT effect_id FROM native_effects WHERE command_id = ?`, r.CommandID).Scan(&commandOwner)
		if err == nil {
			return ErrNativeConflict
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("ring: check native command binding: %w", err)
		}
		session, err := readNativeSession(ctx, tx, r.SessionID)
		if err != nil {
			return err
		}
		if session.Generation != r.Generation {
			return ErrNativeOwner
		}
		if session.ControlState == NativeControlHeld && !nativeUrgent(r.Kind) {
			return ErrNativeHeld
		}
		if r.RetryOfEffectID != "" {
			prior, err := readNativeEffect(ctx, tx, r.RetryOfEffectID)
			if err != nil || prior.SessionID != r.SessionID || prior.Kind != r.Kind ||
				prior.Generation != r.Generation || prior.State != NativeEffectNoEffect {
				return ErrNativeConflict
			}
		}
		command, err := readEntry(ctx, tx, r.CommandID)
		if err != nil || len(command.Body) == 0 || command.Status == StatusFinished ||
			command.Kind != "native."+string(r.Kind) {
			return ErrNativeConflict
		}
		got := sha256.Sum256(command.Body)
		verifiedDigest := hex.EncodeToString(got[:])
		if verifiedDigest != r.CommandBodyDigest {
			return ErrNativeConflict
		}
		var order uint64
		if err := tx.QueryRowContext(ctx, `SELECT coalesce(max(admission_order), 0) + 1 FROM native_effects
			WHERE session_id = ?`, r.SessionID).Scan(&order); err != nil {
			return fmt.Errorf("ring: native effect order: %w", err)
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO native_effects(`+nativeEffectColumns+`)
			VALUES (?, ?, ?, ?, ?, ?, ?, 'prepared', ?, '', '')`,
			r.EffectID, r.CommandID, r.SessionID, r.Generation, r.Kind,
			verifiedDigest, order, r.RetryOfEffectID)
		if err != nil {
			return fmt.Errorf("ring: admit native effect: %w", errors.Join(ErrNativeConflict, err))
		}
		out = r
		out.CommandBodyDigest = verifiedDigest
		out.AdmissionOrder, out.State = order, NativeEffectPrepared
		return nil
	})
	if err != nil {
		return NativeEffectRecord{}, false, err
	}
	return out, duplicate, nil
}

func nativeUrgent(kind NativeEffectKind) bool {
	return kind == NativeEffectInterrupt || kind == NativeEffectStop
}

// ClaimNativeEffect is the only transition that can authorize one external
// call. The caller receives MAY_HAVE_RUN only after this writer job commits.
// A cancelled or uncertain write returns no permission; it must be read back.
func (d *DB) ClaimNativeEffect(ctx context.Context, id, controller string, generation uint64) (NativeEffectRecord, error) {
	if !validNativeRef(id, 128) || !validNativeRef(controller, 128) || generation == 0 {
		return NativeEffectRecord{}, ErrNativeBadInput
	}
	var out NativeEffectRecord
	err := d.s.Write(ctx, func(tx *sql.Tx) error {
		e, err := readNativeEffect(ctx, tx, id)
		if err != nil {
			return err
		}
		if e.State != NativeEffectPrepared {
			return ErrNativeClaimed
		}
		s, err := readNativeSession(ctx, tx, e.SessionID)
		if err != nil {
			return err
		}
		if s.ControllerID != controller || s.Generation != generation || e.Generation != generation {
			return ErrNativeOwner
		}
		if s.ControlState == NativeControlHeld && !nativeUrgent(e.Kind) {
			return ErrNativeHeld
		}
		if !nativeUrgent(e.Kind) {
			var blocked bool
			if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM native_effects
				WHERE session_id = ? AND admission_order < ?
				AND state NOT IN ('confirmed', 'no_effect'))`, e.SessionID, e.AdmissionOrder).Scan(&blocked); err != nil {
				return fmt.Errorf("ring: check native effect order: %w", err)
			}
			if blocked {
				return ErrNativeOrder
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE native_effects SET state = 'may_have_run'
			WHERE effect_id = ? AND state = 'prepared'`, id); err != nil {
			return fmt.Errorf("ring: claim native effect: %w", err)
		}
		e.State = NativeEffectMayHaveRun
		out = e
		return nil
	})
	if err != nil {
		return NativeEffectRecord{}, err
	}
	return out, nil
}
