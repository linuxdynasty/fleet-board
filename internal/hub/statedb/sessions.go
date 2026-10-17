package statedb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/linuxdynasty/fleet-board/internal/store"
)

// sessionsDetailsSQL is migration 21 (plan row 12, the session reads): the
// create-time record of each hub-started session. hub_sessions (migration
// 12) held only the routable stub; the reads need the name, server and
// owner the CreateSession call carried, so CreateSession writes them and
// the reads serve them. Rows from before this migration read back empty.
const sessionsDetailsSQL = `ALTER TABLE hub_sessions ADD COLUMN name TEXT NOT NULL DEFAULT '';
ALTER TABLE hub_sessions ADD COLUMN server TEXT NOT NULL DEFAULT '';
ALTER TABLE hub_sessions ADD COLUMN owner_id TEXT NOT NULL DEFAULT '';
ALTER TABLE hub_sessions ADD COLUMN owner_kind TEXT NOT NULL DEFAULT '';
ALTER TABLE hub_sessions ADD COLUMN owner_name TEXT NOT NULL DEFAULT '';`

// ErrSessionUnknown is ReadSessionRow without a row.
var ErrSessionUnknown = errors.New("statedb: no hub session row for the id")

// SessionRow is one hub_sessions row: the hub-assigned id, the owning
// machine, and the create-time name, server and owner. The Session view
// (hub API reads, row 12) filters, sorts and pages on these columns; the
// proto assembly lives with the API, so this package stays free of
// generated types.
type SessionRow struct {
	SessionID string
	MachineID string
	Name      string
	// Server is the short TmuxServerKind name (DEFAULT, FLEET, PROBE),
	// empty on rows from before migration 21.
	Server string
	// OwnerKind is the short principal kind name (LOCAL_USER, USER,
	// API_TOKEN, DEVICE, UNKNOWN).
	OwnerID, OwnerKind, OwnerName string
	CreatedMillis                 int64
}

// sessionColumns lists every hub_sessions column a SessionRow scan reads.
const sessionColumns = `session_id, machine_id, name, server,
	owner_id, owner_kind, owner_name, create_time`

func scanSessionRow(row rowScanner) (SessionRow, error) {
	var m SessionRow
	err := row.Scan(&m.SessionID, &m.MachineID, &m.Name, &m.Server,
		&m.OwnerID, &m.OwnerKind, &m.OwnerName, &m.CreatedMillis)
	if errors.Is(err, sql.ErrNoRows) {
		return SessionRow{}, ErrSessionUnknown
	}
	if err != nil {
		return SessionRow{}, fmt.Errorf("statedb: read session row: %w", err)
	}
	return m, nil
}

// ReadSessionRow reads one hub-started session on the read pool
// ([ErrSessionUnknown] when there is no row).
func ReadSessionRow(ctx context.Context, s *store.Store, id string) (SessionRow, error) {
	var out SessionRow
	err := s.Read(ctx, func(tx *sql.Tx) error {
		var err error
		out, err = scanSessionRow(tx.QueryRowContext(ctx,
			`SELECT `+sessionColumns+` FROM hub_sessions WHERE session_id = ?`, id))
		return err
	})
	return out, err
}

// ListSessionRows reads every hub-started session on the read pool. Fleets
// hold tens of sessions, so the API filters, sorts and pages in memory
// over this snapshot instead of building per-field SQL.
func ListSessionRows(ctx context.Context, s *store.Store) ([]SessionRow, error) {
	var out []SessionRow
	err := s.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT `+sessionColumns+` FROM hub_sessions`)
		if err != nil {
			return fmt.Errorf("statedb: list session rows: %w", err)
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			m, err := scanSessionRow(rows)
			if err != nil {
				return err
			}
			out = append(out, m)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
