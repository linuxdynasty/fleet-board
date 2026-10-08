package backup

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/store"
)

// Why a copy failed its check.
var (
	// ErrNoIdentity means the copy has no hub_identity row; a restore could
	// not bring the fleet back from it, so it is not kept.
	ErrNoIdentity = errors.New("backup: the copy has no hub_identity row")
	// ErrSchema means the copy's schema_migrations is missing, has a gap, or
	// names a version this binary does not know.
	ErrSchema = errors.New("backup: the copy's schema version is not one this fleetd knows")
	// ErrCorrupt means the copy failed PRAGMA quick_check.
	ErrCorrupt = errors.New("backup: the copy failed quick_check")
)

// verify opens the copy read-only and checks it: quick_check is ok, its
// schema version is between 1 and the last migration this binary knows with
// no gap, and it holds the hub_identity row.
func verify(ctx context.Context, path string) error {
	db, err := store.OpenReadOnly(path)
	if err != nil {
		return fmt.Errorf("backup: open the copy: %w", err)
	}
	defer func() { _ = db.Close() }() // read-only: nothing to flush
	var check string
	if err := db.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&check); err != nil {
		return fmt.Errorf("backup: quick_check: %w", err)
	}
	if check != "ok" {
		return ErrCorrupt
	}
	var n, top int
	err = db.QueryRowContext(ctx, "SELECT count(*), coalesce(max(version), 0) FROM schema_migrations").Scan(&n, &top)
	if err != nil {
		return errors.Join(ErrSchema, err)
	}
	if top < 1 || n != top || top > len(statedb.Migrations()) {
		return fmt.Errorf("%w: version %d with %d rows, this fleetd knows 1 to %d", ErrSchema, top, n, len(statedb.Migrations()))
	}
	var id int
	err = db.QueryRowContext(ctx, "SELECT id FROM hub_identity WHERE id = 1").Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNoIdentity
	}
	if err != nil {
		return fmt.Errorf("backup: read hub_identity: %w", err)
	}
	return nil
}
