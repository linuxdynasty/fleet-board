package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// Migration is one forward-only schema step. Versions start at 1 and rise by
// one. Once a migration has run on any database, never edit it: add the next
// one. Open refuses a file whose recorded SQL differs ([ErrMigrationChanged])
// or whose version is higher than the last one given ([ErrSchemaTooNew]).
type Migration struct {
	Version int
	Name    string
	SQL     string
}

func (m Migration) checksum() string {
	sum := sha256.Sum256([]byte(m.SQL))
	return hex.EncodeToString(sum[:])
}

func validateMigrations(ms []Migration) error {
	for i, m := range ms {
		switch {
		case m.Version != i+1:
			return fmt.Errorf("store: migration %d has version %d, want %d", i, m.Version, i+1)
		case m.Name == "":
			return fmt.Errorf("store: migration %d has no name", m.Version)
		case strings.TrimSpace(m.SQL) == "":
			return fmt.Errorf("store: migration %d has no SQL", m.Version)
		}
	}
	return nil
}

const schemaTable = `CREATE TABLE IF NOT EXISTS schema_migrations(
	version INTEGER PRIMARY KEY,
	name TEXT NOT NULL,
	checksum TEXT NOT NULL,
	applied_time INTEGER NOT NULL
)`

// migrate runs before the writer goroutine starts, on the writer pool, so
// nothing else writes. Each pending migration and its schema_migrations row
// commit in one transaction.
func migrate(ctx context.Context, db *sql.DB, ms []Migration, log *slog.Logger) (int, error) {
	if _, err := db.ExecContext(ctx, schemaTable); err != nil {
		return 0, fmt.Errorf("store: create schema_migrations: %w", err)
	}
	recorded, err := appliedChecksums(ctx, db)
	if err != nil {
		return 0, err
	}
	if len(recorded) > len(ms) {
		return 0, fmt.Errorf("%w: file at version %d, binary knows %d", ErrSchemaTooNew, len(recorded), len(ms))
	}
	for i, sum := range recorded {
		if sum != ms[i].checksum() {
			return 0, fmt.Errorf("%w: version %d (%s)", ErrMigrationChanged, ms[i].Version, ms[i].Name)
		}
	}
	for _, m := range ms[len(recorded):] {
		if err := applyMigration(ctx, db, m); err != nil {
			return 0, err
		}
		log.Info("store.migrated", "schema_version", m.Version)
	}
	return len(ms), nil
}

// appliedChecksums returns the checksums by version, refusing a gap.
func appliedChecksums(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx, "SELECT version, checksum FROM schema_migrations ORDER BY version")
	if err != nil {
		return nil, fmt.Errorf("store: read schema_migrations: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var sums []string
	for rows.Next() {
		var v int
		var sum string
		if err := rows.Scan(&v, &sum); err != nil {
			return nil, fmt.Errorf("store: read schema_migrations: %w", err)
		}
		if v != len(sums)+1 {
			return nil, fmt.Errorf("store: schema_migrations has version %d after %d", v, len(sums))
		}
		sums = append(sums, sum)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: read schema_migrations: %w", err)
	}
	return sums, nil
}

func applyMigration(ctx context.Context, db *sql.DB, m Migration) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: migration %d: begin: %w", m.Version, err)
	}
	_, err = tx.ExecContext(ctx, m.SQL)
	if err == nil {
		_, err = tx.ExecContext(ctx,
			"INSERT INTO schema_migrations(version, name, checksum, applied_time) VALUES (?, ?, ?, ?)",
			m.Version, m.Name, m.checksum(), time.Now().UnixMilli())
	}
	if err != nil {
		return errors.Join(fmt.Errorf("store: migration %d (%s): %w", m.Version, m.Name, err), tx.Rollback())
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: migration %d: commit: %w", m.Version, err)
	}
	return nil
}
