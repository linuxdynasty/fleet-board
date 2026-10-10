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
	"strings"

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
	}, {
		Version: 2,
		Name:    "enroll_tokens",
		SQL: `CREATE TABLE enroll_tokens(
	id INTEGER PRIMARY KEY,
	secret_hash BLOB NOT NULL UNIQUE CHECK (length(secret_hash) = 32),
	machine_name TEXT NOT NULL CHECK (machine_name <> ''),
	expected_hostname TEXT NOT NULL,
	labels TEXT NOT NULL DEFAULT '[]',
	start_drained INTEGER NOT NULL CHECK (start_drained IN (0, 1)),
	not_after_time INTEGER NOT NULL,
	used_time INTEGER,
	previous_machine TEXT,
	created_time INTEGER NOT NULL,
	principal TEXT NOT NULL CHECK (principal <> ''),
	request_id TEXT NOT NULL CHECK (request_id <> ''),
	request_hash BLOB NOT NULL CHECK (length(request_hash) = 32),
	UNIQUE (principal, request_id)
) STRICT;`,
	}, {
		Version: 3,
		Name:    "machines_serials_enroll_responses",
		SQL:     machinesSQL,
	}, {
		Version: 4,
		Name:    "cert_renewals",
		SQL:     renewalsSQL,
	}, {
		Version: 5,
		Name:    "machine_connections",
		SQL:     connectionsSQL,
	}, {
		Version: 6,
		Name:    "audit_log_hub_seq",
		SQL:     auditSQL,
	}, {
		Version: 7,
		Name:    "restore_reconcile",
		SQL:     restoreSQL,
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
		// A umask cannot widen the mode, but may narrow it.
		if err := os.Chmod(dir, 0o700); err != nil { //nolint:gosec // G302: a folder needs the x bit; 0700 is owner-only
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
	// SyncDir makes the new name durable. Default: fsync of the folder.
	SyncDir func(dir string) error
}

// ErrCreatedNotSynced means state.db was created, complete with its
// hub_identity row, but the state folder could not be synced afterwards, so
// the new name may not survive a power loss.
var ErrCreatedNotSynced = errors.New("statedb: state.db was created, but syncing the state folder failed")

// tempPrefix starts the name of the temporary file [Create] builds.
const tempPrefix = ".state.db.init-"

// RestoreTempPrefix starts the name of the temporary file fleetd restore
// builds next to state.db before it renames it into place.
const RestoreTempPrefix = ".state.db.restore-"

// Create builds state.db with the hub_identity row so that it never exists
// without it: a temporary file (mode 0600 before SQLite opens it) gets the
// migrations and the row through the store's writer, is closed (which
// checkpoints and removes the WAL), and is then linked to state.db, which
// fails if state.db exists ([ErrExists]). A link, unlike a rename, never
// replaces an existing state.db. A failure before the link leaves no
// state.db and no temporary file; a failure syncing the folder after it
// leaves a complete state.db and is [ErrCreatedNotSynced]. Call it with the
// state folder's lock held.
func Create(ctx context.Context, dir string, id Identity, opts CreateOptions) (err error) {
	if opts.Link == nil {
		opts.Link = os.Link
	}
	if opts.SyncDir == nil {
		opts.SyncDir = syncDir
	}
	final := Path(dir)
	if _, err := os.Lstat(final); err == nil {
		return ErrExists
	}
	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		return fmt.Errorf("statedb: temp name: %w", err)
	}
	tmp := filepath.Join(dir, tempPrefix+hex.EncodeToString(suffix))
	f, err := os.OpenFile(tmp, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // G304: tmp is a fixed prefix plus random hex inside the operator's configured state folder
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
	if err := opts.SyncDir(dir); err != nil {
		return fmt.Errorf("%w (%s): %w", ErrCreatedNotSynced, final, err)
	}
	return nil
}

// RemoveLeftovers deletes the temporary files of an earlier [Create] or
// fleetd restore that crashed (the file, its SQLite side files, or a second
// link to state.db left between the link and the cleanup) and returns how
// many it removed. Call it with the state folder's lock held, so no Create
// or restore is running.
func RemoveLeftovers(dir string) (int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, fmt.Errorf("statedb: list state folder: %w", err)
	}
	n := 0
	for _, e := range entries {
		temp := strings.HasPrefix(e.Name(), tempPrefix) || strings.HasPrefix(e.Name(), RestoreTempPrefix)
		if !temp || e.IsDir() {
			continue
		}
		if err := os.Remove(filepath.Join(dir, e.Name())); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return n, fmt.Errorf("statedb: remove leftover temp file: %w", err)
		}
		n++
	}
	return n, nil
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
	werr := s.Write(ctx, insertIdentity(ctx, id))
	if err := errors.Join(werr, s.Close()); err != nil {
		return err
	}
	if fi, err := os.Stat(path + "-wal"); err == nil && fi.Size() > 0 {
		return errors.New("statedb: the new store's WAL was not checkpointed on close")
	}
	f, err := os.Open(path) //nolint:gosec // G304: path is the temp state file Init made in the operator's configured state folder
	if err != nil {
		return fmt.Errorf("statedb: sync new store: %w", err)
	}
	return errors.Join(f.Sync(), f.Close())
}

func syncDir(dir string) error {
	d, err := os.Open(dir) //nolint:gosec // G304: dir is the operator's configured state folder, opened only to fsync it
	if err != nil {
		return fmt.Errorf("statedb: sync state folder: %w", err)
	}
	return errors.Join(d.Sync(), d.Close())
}
