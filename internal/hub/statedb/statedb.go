package statedb

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/linuxdynasty/fleet-board/internal/store"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

// FileName is the hub store inside the state folder (02j).
const FileName = "state.db"

// ErrExists means state.db is already there; init never replaces a store.
var ErrExists = errors.New("statedb: state.db already exists")

// Path is state.db inside dir.
func Path(dir string) string { return filepath.Join(dir, FileName) }

// Migrations is the ordered, forward-only schema of state.db. Never edit an
// entry that has shipped; append the next one.
func Migrations() []store.Migration {
	return []store.Migration{{
		Version: 1,
		Name:    "hub_identity",
		SQL: `CREATE TABLE hub_identity(
	id INTEGER PRIMARY KEY CHECK (id = 1),
	fleet_id TEXT NOT NULL CHECK (fleet_id <> ''),
	ca_cert BLOB NOT NULL,
	ca_key BLOB NOT NULL,
	server_cert BLOB NOT NULL,
	server_key BLOB NOT NULL,
	server_names TEXT NOT NULL,
	account_salt BLOB NOT NULL CHECK (length(account_salt) = 32),
	salt_version INTEGER NOT NULL CHECK (salt_version >= 1),
	settings_epoch TEXT NOT NULL CHECK (settings_epoch <> ''),
	hub_addresses TEXT NOT NULL DEFAULT '[]',
	created_time INTEGER NOT NULL
) STRICT;`,
	}}
}

// EnsureDir creates the state folder with mode 0700 (and missing parents), or
// checks an existing one: it must be a folder owned by this user with no
// group or other access. It never loosens or tightens an existing folder.
func EnsureDir(dir string) error {
	err := os.Mkdir(dir, 0o700)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
			return fmt.Errorf("statedb: create state folder: %w", err)
		}
		return EnsureDir(dir)
	case err == nil:
		if err := os.Chmod(dir, 0o700); err != nil { // a umask cannot widen it, but may narrow it
			return fmt.Errorf("statedb: chmod state folder: %w", err)
		}
	case !errors.Is(err, fs.ErrExist):
		return fmt.Errorf("statedb: create state folder: %w", err)
	}
	return CheckDir(dir)
}

// Open opens an existing state.db read-write, never creating one: the folder
// and the file must pass the mode checks ([ErrLooseMode]); a missing file is
// store.ErrMissing. It applies pending migrations.
func Open(ctx context.Context, dir string, tel *telemetry.Telemetry) (*store.Store, error) {
	if err := CheckDir(dir); err != nil {
		return nil, err
	}
	if err := checkFile(Path(dir)); err != nil {
		return nil, err
	}
	return store.Open(ctx, store.Options{
		Path: Path(dir), Name: "state", Migrations: Migrations(), MustExist: true, Telemetry: tel,
	})
}

// CreateOptions adjust [Create]. The zero value is the production behavior.
type CreateOptions struct {
	Telemetry *telemetry.Telemetry
	// Link publishes the finished file under its final name and must fail
	// when that name exists. Default os.Link.
	Link func(oldname, newname string) error
}

// Create builds state.db with the hub_identity row so that it never exists
// without it: a temporary file (mode 0600 before SQLite opens it) gets the
// migrations and the row through the store's writer, is closed (which
// checkpoints and removes the WAL), and is then linked to state.db, which
// fails if state.db exists ([ErrExists]). Any failure leaves no state.db and
// no temporary file. Call it with the state folder's lock held.
func Create(ctx context.Context, dir string, id Identity, opts CreateOptions) (err error) {
	if opts.Link == nil {
		opts.Link = os.Link
	}
	final := Path(dir)
	if _, err := os.Lstat(final); err == nil {
		return ErrExists
	}
	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		return fmt.Errorf("statedb: temp name: %w", err)
	}
	tmp := filepath.Join(dir, ".state.db.init-"+hex.EncodeToString(suffix))
	f, err := os.OpenFile(tmp, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("statedb: create temp file: %w", err)
	}
	defer func() {
		for _, p := range []string{tmp, tmp + "-wal", tmp + "-shm", tmp + "-journal"} {
			if rmErr := os.Remove(p); rmErr != nil && !errors.Is(rmErr, fs.ErrNotExist) {
				err = errors.Join(err, fmt.Errorf("statedb: remove temp file: %w", rmErr))
			}
		}
	}()
	if err := f.Close(); err != nil {
		return fmt.Errorf("statedb: create temp file: %w", err)
	}
	if err := build(ctx, tmp, id, opts.Telemetry); err != nil {
		return err
	}
	if err := opts.Link(tmp, final); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return ErrExists
		}
		return fmt.Errorf("statedb: publish state.db: %w", err)
	}
	return syncDir(dir)
}

// build migrates the file at path, writes the identity row and closes it,
// then checks the WAL is gone so the main file holds every commit.
func build(ctx context.Context, path string, id Identity, tel *telemetry.Telemetry) error {
	s, err := store.Open(ctx, store.Options{
		Path: path, Name: "state", Migrations: Migrations(), MustExist: true, Telemetry: tel,
	})
	if err != nil {
		return fmt.Errorf("statedb: open new store: %w", err)
	}
	werr := s.Write(ctx, insertIdentity(id))
	if err := errors.Join(werr, s.Close()); err != nil {
		return err
	}
	if fi, err := os.Stat(path + "-wal"); err == nil && fi.Size() > 0 {
		return errors.New("statedb: the new store's WAL was not checkpointed on close")
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("statedb: sync new store: %w", err)
	}
	return errors.Join(f.Sync(), f.Close())
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("statedb: sync state folder: %w", err)
	}
	return errors.Join(d.Sync(), d.Close())
}
