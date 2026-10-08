package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// readOnlyDSN names path as an escaped file: URI with SQLite's mode=ro, so
// the connection can never create or write the file. queryOnly adds
// query_only, which VACUUM INTO refuses (it counts as a write), so the copy
// connection leaves it off.
func readOnlyDSN(path string, queryOnly bool) string {
	p := []string{"mode=ro", "_pragma=busy_timeout(5000)"}
	if queryOnly {
		p = append(p, "_pragma=query_only(1)")
	}
	return (&url.URL{Scheme: "file", Path: path}).String() + "?" + strings.Join(p, "&")
}

// VacuumInto writes a consistent copy of the database at src into dst with
// VACUUM INTO. It opens src on its own read-only connection (mode=ro), not
// the store's writer or read pool, and runs outside any transaction, so it
// works whether or not a [Store] has src open (its committed WAL rows are
// copied too) and never writes src. dst must not exist or must be empty;
// SQLite refuses anything else, so a copy never overwrites a file. The caller
// syncs dst.
func VacuumInto(ctx context.Context, src, dst string) error {
	if strings.ContainsRune(src, '?') {
		return errors.New("store: source path must not contain '?'")
	}
	db, err := sql.Open("sqlite", readOnlyDSN(src, false))
	if err != nil {
		return fmt.Errorf("store: open copy source: %w", err)
	}
	defer func() { _ = db.Close() }() // read-only: nothing to flush
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("store: open copy source: %w", err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, "VACUUM INTO ?", dst); err != nil {
		return fmt.Errorf("store: vacuum into: %w", err)
	}
	return nil
}

// OpenReadOnly opens an existing database file read-only (mode=ro and
// query_only), for checking a copy such as a backup. It never creates the
// file: a missing one is an error. The caller closes the pool.
func OpenReadOnly(path string) (*sql.DB, error) {
	if strings.ContainsRune(path, '?') {
		return nil, errors.New("store: path must not contain '?'")
	}
	db, err := sql.Open("sqlite", readOnlyDSN(path, true))
	if err != nil {
		return nil, fmt.Errorf("store: open read-only: %w", err)
	}
	if err := db.PingContext(context.Background()); err != nil {
		return nil, errors.Join(fmt.Errorf("store: open read-only: %w", err), db.Close())
	}
	return db, nil
}
