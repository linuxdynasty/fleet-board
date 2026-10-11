package ring

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ErrSettingsOrder means a Settings save with no epoch, or with a generation
// below the held one under the same epoch (02-contracts "Settings order");
// nothing was saved.
var ErrSettingsOrder = errors.New("ring: settings are older than the ones held")

// Held is the settings row: what the agent holds of the hub's settings.
type Held struct {
	// Epoch and Generation are Hello.settings_epoch and settings_generation.
	Epoch      string
	Generation uint64
	// Settings is the hub's Settings message as the agent keeps it
	// (encoded); Ack the SettingsAck last sent for it (encoded).
	Settings []byte
	Ack      []byte
	// PolicyHash is the hash of the policy file the ack was merged under.
	PolicyHash string
	// Applied is when it was saved (agent clock).
	Applied time.Time
}

// HeldSettings reads the settings row; ok is false before any Settings was
// saved.
func (d *DB) HeldSettings(ctx context.Context) (Held, bool, error) {
	var h Held
	var ok bool
	err := d.s.Read(ctx, func(tx *sql.Tx) error {
		var gen, at int64
		err := tx.QueryRowContext(ctx, `SELECT settings_epoch, generation, settings, ack, policy_hash, applied_time
			FROM settings_held WHERE id = 1`).Scan(&h.Epoch, &gen, &h.Settings, &h.Ack, &h.PolicyHash, &at)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("ring: read held settings: %w", err)
		}
		ok, h.Generation, h.Applied = true, toUint(gen), time.UnixMilli(at)
		return nil
	})
	if err != nil || !ok {
		return Held{}, false, err
	}
	return h, true, nil
}

// SaveSettings stores h as the held settings in one store job, before the
// SettingsAck is sent (03e "Persistence"). The same generation may be saved
// again (a restart merges it under a new policy file); a lower one under the
// same epoch, or no epoch, is [ErrSettingsOrder].
func (d *DB) SaveSettings(ctx context.Context, h Held) error {
	gen, err := toInt("generation", h.Generation)
	if err != nil {
		return err
	}
	if h.Epoch == "" {
		return fmt.Errorf("%w: no epoch", ErrSettingsOrder)
	}
	if h.Settings == nil {
		h.Settings = []byte{}
	}
	if h.Ack == nil {
		h.Ack = []byte{}
	}
	return d.s.Write(ctx, func(tx *sql.Tx) error {
		var epoch string
		var held int64
		err := tx.QueryRowContext(ctx, `SELECT settings_epoch, generation FROM settings_held WHERE id = 1`).Scan(&epoch, &held)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			_, err = tx.ExecContext(ctx, `INSERT INTO settings_held(id, settings_epoch, generation, settings, ack, policy_hash, applied_time)
				VALUES (1, ?, ?, ?, ?, ?, ?)`, h.Epoch, gen, h.Settings, h.Ack, h.PolicyHash, h.Applied.UnixMilli())
		case err != nil:
		case epoch == h.Epoch && gen < held:
			return fmt.Errorf("%w: generation %d after %d", ErrSettingsOrder, h.Generation, held)
		default:
			_, err = tx.ExecContext(ctx, `UPDATE settings_held SET settings_epoch = ?, generation = ?, settings = ?, ack = ?,
				policy_hash = ?, applied_time = ? WHERE id = 1`, h.Epoch, gen, h.Settings, h.Ack, h.PolicyHash, h.Applied.UnixMilli())
		}
		if err != nil {
			return fmt.Errorf("ring: save settings: %w", err)
		}
		return nil
	})
}
