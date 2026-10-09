package ring

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/store"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

// FileName is the agent store inside the agent state folder (04 Q9).
const FileName = "agent.db"

// ErrLooseMode means agent.db (or its -wal or -shm) is not a regular file
// private to its owner (mode 0600 or narrower).
var ErrLooseMode = errors.New("ring: agent.db is open to other users")

// Path is agent.db inside the state folder dir.
func Path(dir string) string { return filepath.Join(dir, FileName) }

// Options adjust [Open]. The zero value is the production behavior.
type Options struct {
	// Telemetry receives the store's metrics and log lines (db="agent").
	Telemetry *telemetry.Telemetry
	// Random mints install_id; nil means crypto/rand.
	Random io.Reader
	// Now stamps created_time; nil means time.Now.
	Now func() time.Time
}

// DB is agent.db: one store with its single writer goroutine.
type DB struct {
	s *store.Store
}

// Open creates agent.db (mode 0600) in dir when it is missing, checks it is
// private, applies pending migrations and mints install_id once. dir is the
// agent state folder; the caller creates and checks it (mode 0700).
func Open(ctx context.Context, dir string, opts Options) (*DB, error) {
	if opts.Random == nil {
		opts.Random = rand.Reader
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	path := Path(dir)
	if err := createPrivate(path); err != nil {
		return nil, err
	}
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		if err := checkPrivate(p); err != nil {
			return nil, err
		}
	}
	s, err := store.Open(ctx, store.Options{
		Path: path, Name: "agent", Migrations: Migrations(), MustExist: true, Telemetry: opts.Telemetry,
	})
	if err != nil {
		return nil, fmt.Errorf("ring: open agent.db: %w", err)
	}
	d := &DB{s: s}
	if err := d.mintInstallID(ctx, opts.Random, opts.Now()); err != nil {
		return nil, errors.Join(err, s.Close())
	}
	return d, nil
}

// SchemaVersion is the highest migration applied to agent.db.
func (d *DB) SchemaVersion() int { return d.s.SchemaVersion() }

// Close commits every queued write and closes the file.
func (d *DB) Close() error { return d.s.Close() }

// createPrivate makes an empty agent.db with mode 0600 before SQLite opens
// it, so the file never exists with a wider mode. SQLite gives the -wal and
// -shm files the mode of the database file.
func createPrivate(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // G304: path is agent.db inside the agent's own state folder
	if errors.Is(err, fs.ErrExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("ring: create agent.db: %w", err)
	}
	return f.Close()
}

// checkPrivate refuses a symlink, a non-regular file or any group or other
// permission bit. A missing file passes.
func checkPrivate(path string) error {
	fi, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("ring: %w", err)
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%w: %s is not a regular file", ErrLooseMode, filepath.Base(path))
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		return fmt.Errorf("%w: %s is mode %04o; run chmod 600 on it", ErrLooseMode, filepath.Base(path), perm)
	}
	return nil
}

// mintInstallID writes the one agent_state row with a fresh install_id when
// the file has none; an existing row (and its install_id) is kept.
func (d *DB) mintInstallID(ctx context.Context, random io.Reader, now time.Time) error {
	b := make([]byte, 16)
	if _, err := io.ReadFull(random, b); err != nil {
		return fmt.Errorf("ring: install_id: %w", err)
	}
	return d.s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO agent_state(id, install_id, created_time) VALUES (1, ?, ?) ON CONFLICT(id) DO NOTHING`,
			hex.EncodeToString(b), now.UnixMilli())
		return err
	})
}

// toInt converts a counter for SQLite's signed INTEGER.
func toInt(name string, v uint64) (int64, error) {
	if v > math.MaxInt64 {
		return 0, fmt.Errorf("ring: %s %d does not fit in agent.db", name, v)
	}
	return int64(v), nil
}

// toUint converts a stored counter back; the schema keeps it non-negative.
func toUint(v int64) uint64 {
	if v < 0 {
		return 0
	}
	return uint64(v)
}
