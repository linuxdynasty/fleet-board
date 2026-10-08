package statedb

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/store"
)

// RevokedSerial is one revoked certificate serial (lowercase hex) and the
// certificate's not_after, after which the revocation no longer matters.
type RevokedSerial struct {
	Serial   string
	NotAfter time.Time
}

// RevokeSerials sets revoked_time to at on every listed serial that is in
// cert_serials and not yet revoked, in one writer job, and returns how many
// changed. A serial state.db does not know is skipped: the revoked-serials
// file keeps it. Machine state is not touched (plan row 13a).
func RevokeSerials(ctx context.Context, s *store.Store, serials []string, at time.Time) (int, error) {
	jctx := context.WithoutCancel(ctx) // see insertIdentity
	var n int
	err := s.Write(ctx, func(tx *sql.Tx) error {
		n = 0
		for _, serial := range serials {
			res, err := tx.ExecContext(jctx, `UPDATE cert_serials SET revoked_time = ? WHERE serial = ? AND revoked_time IS NULL`,
				at.UnixMilli(), serial)
			if err != nil {
				return fmt.Errorf("statedb: revoke serial: %w", err)
			}
			changed, err := res.RowsAffected()
			if err != nil {
				return fmt.Errorf("statedb: revoke serial: %w", err)
			}
			n += int(changed)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return n, nil
}

// RevokedSerials lists the revoked serials whose certificate is still valid
// at now: the ones a handshake must refuse.
func RevokedSerials(ctx context.Context, s *store.Store, now time.Time) ([]RevokedSerial, error) {
	var out []RevokedSerial
	err := s.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT serial, not_after_time FROM cert_serials
			WHERE revoked_time IS NOT NULL AND not_after_time > ? ORDER BY serial`, now.UnixMilli())
		if err != nil {
			return fmt.Errorf("statedb: list revoked serials: %w", err)
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var r RevokedSerial
			var end int64
			if err := rows.Scan(&r.Serial, &end); err != nil {
				return fmt.Errorf("statedb: list revoked serials: %w", err)
			}
			r.NotAfter = time.UnixMilli(end)
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}
