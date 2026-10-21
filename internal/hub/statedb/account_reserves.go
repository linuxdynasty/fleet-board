package statedb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	"github.com/linuxdynasty/fleet-board/internal/store"
)

// accountReservesSQL is migration 26 (plan row 13): the
// reserve-effective-time history behind the subscription-efficiency
// usable line (05 section 3: the usable line uses the reserve as it was
// when the window ended). Neither the accounts row (the current spec
// only) nor the account.* audit detail ({id, version}) nor the 24-hour
// config_request_log keeps past specs, so the history needs its own
// table: one row per account per reserve change, stamped at the write.
const accountReservesSQL = `CREATE TABLE account_reserve_history(
account_id TEXT NOT NULL CHECK (account_id <> ''),
from_ms INTEGER NOT NULL,
reserve_percent INTEGER NOT NULL CHECK (reserve_percent >= 0),
PRIMARY KEY (account_id, from_ms)
) STRICT;`

const (
	// defaultAccountReserve is the quota kept for interactive use when
	// an account has no stored spec (a new account gets 10, 05
	// section 3).
	defaultAccountReserve = 10
	// maxAccountReserve caps a stored reserve the way pacing does
	// (defaults.md: 0 to 50).
	maxAccountReserve = 50
)

// AccountReserveChange is the reserve in force from From on.
type AccountReserveChange struct {
	AccountID string
	From      time.Time
	Percent   uint32
}

// ReservePercentOfSpec reads the reserve from an account spec, capped
// at 50 like pacing; an unreadable spec reads the 10-point default.
func ReservePercentOfSpec(spec []byte) uint32 {
	var parsed hubv1.AccountSpec
	if err := protojson.Unmarshal(spec, &parsed); err != nil {
		return defaultAccountReserve
	}
	if parsed.GetReservePercent() > maxAccountReserve {
		return maxAccountReserve
	}
	return parsed.GetReservePercent()
}

// recordAccountReserveTx appends a history row stamped at the write
// when the reserve in spec differs from the latest recorded one; a
// create records the first row, a reserve-keeping update records
// nothing. It runs inside the account write's own writer transaction.
func recordAccountReserveTx(ctx context.Context, tx *sql.Tx, accountID string, spec []byte, at time.Time) error {
	percent := ReservePercentOfSpec(spec)
	var latest sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT reserve_percent FROM account_reserve_history
		WHERE account_id = ? ORDER BY from_ms DESC LIMIT 1`, accountID).Scan(&latest); err != nil &&
		!errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("statedb: read reserve history: %w", err)
	}
	if latest.Valid && uint32(latest.Int64) == percent { //nolint:gosec // G115: percents are capped at 50
		return nil
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO account_reserve_history(account_id, from_ms, reserve_percent)
		VALUES (?, ?, ?)
		ON CONFLICT(account_id, from_ms) DO UPDATE SET reserve_percent = excluded.reserve_percent`,
		accountID, at.UnixMilli(), percent); err != nil {
		return fmt.Errorf("statedb: record reserve history: %w", err)
	}
	return nil
}

// ListAccountReserveHistory reads the reserve history of one account
// (empty accountID reads every account), oldest first, on the read
// pool. An account created before migration 26 has no rows: callers
// fall back to its current reserve.
func ListAccountReserveHistory(ctx context.Context, s *store.Store, accountID string) ([]AccountReserveChange, error) {
	var out []AccountReserveChange
	err := s.Read(ctx, func(tx *sql.Tx) error {
		query := `SELECT account_id, from_ms, reserve_percent FROM account_reserve_history ORDER BY from_ms`
		args := []any{}
		if accountID != "" {
			query = `SELECT account_id, from_ms, reserve_percent FROM account_reserve_history
				WHERE account_id = ? ORDER BY from_ms`
			args = append(args, accountID)
		}
		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return fmt.Errorf("statedb: list reserve history: %w", err)
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var c AccountReserveChange
			var fromMs, percent int64
			if err := rows.Scan(&c.AccountID, &fromMs, &percent); err != nil {
				return fmt.Errorf("statedb: read reserve history: %w", err)
			}
			c.From = time.UnixMilli(fromMs).UTC()
			c.Percent = uint32(percent) //nolint:gosec // G115: percents are capped at 50
			out = append(out, c)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
