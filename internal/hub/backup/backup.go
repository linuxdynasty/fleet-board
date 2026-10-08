package backup

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/store"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

// DirName is the backups folder inside the state folder (02j).
const DirName = "backups"

// Trigger says why a copy was made; it is the trigger field of the backup
// log events and the first word of the file name.
type Trigger string

// The two triggers.
const (
	Nightly Trigger = "nightly" // the job inside fleetd run
	Manual  Trigger = "manual"  // fleetd backup
)

// step is the closed step field of backup.failed.
type step string

const (
	stepPassphrase step = "passphrase"
	stepSource     step = "source"
	stepDir        step = "dir"
	stepCopy       step = "copy"
	stepVerify     step = "verify"
	stepEncrypt    step = "encrypt"
	stepPublish    step = "publish"
	stepCleanup    step = "cleanup"
	stepPrune      step = "prune"
)

// Temporary names. A plain copy is built in backups/; the plaintext of an
// encrypted copy is built in the state folder (0700) and never reaches
// backups/.
const (
	partialPrefix = ".partial-"
	plainPrefix   = ".backup-plain-"
)

// Options configure [Take]. The zero values of Now and Telemetry are the
// production behavior.
type Options struct {
	// StateDir is the state folder; the source is its state.db and copies go
	// to its backups folder.
	StateDir string
	Trigger  Trigger
	// Terminal, when set, encrypts the copy with age under a passphrase read
	// from it (asked twice). Only a Manual copy may be encrypted.
	Terminal Terminal
	// Now stamps the file name (in UTC); nil means time.Now.
	Now func() time.Time
	// Telemetry receives backup.completed and backup.failed; nil turns them off.
	Telemetry *telemetry.Telemetry
	// WorkFactor is age's scrypt work factor (log2); 0 is age's default.
	// Tests lower it.
	WorkFactor int
}

// Result is a published backup.
type Result struct {
	Path      string
	Bytes     int64
	Encrypted bool
}

// Take makes one backup of <state_dir>/state.db into <state_dir>/backups/,
// named nightly-<UTC stamp>.db, manual-<UTC stamp>.db or, encrypted,
// manual-<UTC stamp>.db.age. It takes no lock and works whether or not
// fleetd run has the store open: the copy is a VACUUM INTO on a read-only
// connection of its own (store.VacuumInto) into a file pre-created empty
// with mode 0600. The copy is synced and checked (it opens, passes
// quick_check, has a schema version this binary knows and holds
// hub_identity) before it is published under its final name, which never
// replaces an existing file, and the folder is synced. Every failure logs
// backup.failed with its step and leaves no temporary file behind; success
// logs backup.completed.
func Take(ctx context.Context, opts Options) (Result, error) {
	if opts.Trigger != Nightly && opts.Trigger != Manual {
		return Result{}, fmt.Errorf("backup: unknown trigger %q", opts.Trigger)
	}
	if opts.Trigger == Nightly && opts.Terminal != nil {
		return Result{}, errors.New("backup: a nightly copy is never encrypted")
	}
	log := logger(opts.Telemetry)
	res, st, err := take(ctx, opts)
	if err != nil {
		log.Error("backup.failed", "trigger", string(opts.Trigger), "step", string(st), "err_kind", errKind(err))
		return Result{}, err
	}
	log.Info("backup.completed", "trigger", string(opts.Trigger), "bytes", res.Bytes, "encrypted", res.Encrypted)
	return res, nil
}

func logger(tel *telemetry.Telemetry) *slog.Logger {
	if tel == nil {
		return slog.New(slog.DiscardHandler)
	}
	return tel.Logger.With("component", "backup")
}

func take(ctx context.Context, o Options) (res Result, st step, err error) {
	var pass []byte
	if o.Terminal != nil {
		if pass, err = readPassphrase(o.Terminal); err != nil {
			return res, stepPassphrase, err
		}
		defer clear(pass)
	}
	if err := checkSource(o.StateDir); err != nil {
		return res, stepSource, err
	}
	dir := filepath.Join(o.StateDir, DirName)
	if err := ensureDir(o.StateDir, dir); err != nil {
		return res, stepDir, err
	}
	now := time.Now
	if o.Now != nil {
		now = o.Now
	}
	encrypted := pass != nil
	final := filepath.Join(dir, fileName(o.Trigger, now(), encrypted))

	var temps []string
	defer func() {
		for _, p := range temps {
			if rmErr := removeAll(p); rmErr != nil && err == nil {
				res, st, err = Result{}, stepCleanup, rmErr
			}
		}
	}()
	plainDir, prefix := dir, partialPrefix
	if encrypted {
		plainDir, prefix = o.StateDir, plainPrefix
	}
	plain, err := tempName(plainDir, prefix)
	if err != nil {
		return res, stepCopy, err
	}
	temps = append(temps, plain)
	if err := copyStore(ctx, statedb.Path(o.StateDir), plain); err != nil {
		return res, stepCopy, err
	}
	if err := verify(ctx, plain); err != nil {
		return res, stepVerify, err
	}
	publish := plain
	if encrypted {
		if publish, err = tempName(dir, partialPrefix); err != nil {
			return res, stepEncrypt, err
		}
		temps = append(temps, publish)
		if err := encrypt(ctx, plain, publish, pass, o.WorkFactor); err != nil {
			return res, stepEncrypt, err
		}
	}
	if err := link(publish, final); err != nil {
		return res, stepPublish, err
	}
	fi, err := os.Stat(final)
	if err != nil {
		return res, stepPublish, fmt.Errorf("backup: stat the published copy: %w", err)
	}
	return Result{Path: final, Bytes: fi.Size(), Encrypted: encrypted}, "", nil
}

// fileName is <trigger>-<UTC stamp>.db, with .age when encrypted.
func fileName(t Trigger, at time.Time, encrypted bool) string {
	n := string(t) + "-" + at.UTC().Format(stampLayout) + ".db"
	if encrypted {
		n += ".age"
	}
	return n
}

const stampLayout = "20060102T150405Z"

// checkSource checks the state folder and state.db the way fleetd run does
// (owned by this user, no group or other access), without opening the store.
func checkSource(stateDir string) error {
	if err := statedb.CheckDir(stateDir); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			err = fmt.Errorf("backup: no state folder %s; run fleetd init first: %w", stateDir, err)
		}
		return err
	}
	err := statedb.CheckPrivateFile(statedb.Path(stateDir))
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("backup: %s does not exist; run fleetd init first: %w", statedb.Path(stateDir), err)
	}
	return err
}

// ensureDir creates backups/ with mode 0700 (and syncs the state folder so
// the new entry lasts), or checks an existing one is a folder private to
// this user. It never changes the mode of a folder it did not create.
func ensureDir(stateDir, dir string) error {
	err := os.Mkdir(dir, 0o700)
	switch {
	case err == nil:
		// A umask cannot widen the mode, but may narrow it.
		if err := os.Chmod(dir, 0o700); err != nil { //nolint:gosec // G302: a folder needs the x bit; 0700 is owner-only
			return fmt.Errorf("backup: chmod backups folder: %w", err)
		}
		if err := syncDir(stateDir); err != nil {
			return err
		}
	case !errors.Is(err, fs.ErrExist):
		return fmt.Errorf("backup: create backups folder: %w", err)
	}
	return statedb.CheckDir(dir)
}

// tempName is dir/<prefix><random hex>; the file does not exist yet.
func tempName(dir, prefix string) (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("backup: temp name: %w", err)
	}
	return filepath.Join(dir, prefix+hex.EncodeToString(b)), nil
}

// copyStore pre-creates dst empty (O_EXCL, 0600) so the copy never has a
// wider mode, fills it with VACUUM INTO and syncs it.
func copyStore(ctx context.Context, src, dst string) error {
	f, err := os.OpenFile(dst, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // G304: dst is a fixed prefix plus random hex in the operator's state folder
	if err != nil {
		return fmt.Errorf("backup: create the copy: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("backup: create the copy: %w", err)
	}
	if err := store.VacuumInto(ctx, src, dst); err != nil {
		return fmt.Errorf("backup: %w", err)
	}
	return syncFile(dst)
}

// link publishes tmp under final, failing (fs.ErrExist) rather than replace
// a file already there, then drops the temporary name and syncs the folder
// so both changes last.
func link(tmp, final string) error {
	if err := os.Link(tmp, final); err != nil {
		return fmt.Errorf("backup: publish: %w", err)
	}
	if err := os.Remove(tmp); err != nil {
		return fmt.Errorf("backup: publish: %w", err)
	}
	return syncDir(filepath.Dir(final))
}

// removeAll removes a temporary file and any SQLite side file next to it.
func removeAll(path string) error {
	var errs []error
	for _, p := range []string{path, path + "-wal", path + "-shm", path + "-journal"} {
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, fmt.Errorf("backup: remove temporary file: %w", err))
		}
	}
	return errors.Join(errs...)
}

func syncFile(path string) error {
	f, err := os.Open(path) //nolint:gosec // G304: a backup file in the operator's state folder, opened only to fsync it
	if err != nil {
		return fmt.Errorf("backup: sync: %w", err)
	}
	return errors.Join(f.Sync(), f.Close())
}

func syncDir(dir string) error {
	d, err := os.Open(dir) //nolint:gosec // G304: the operator's state or backups folder, opened only to fsync it
	if err != nil {
		return fmt.Errorf("backup: sync folder: %w", err)
	}
	return errors.Join(d.Sync(), d.Close())
}

// errKind is the closed err_kind class of 05b.
func errKind(err error) string {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, ErrNotTerminal), errors.Is(err, ErrPassphraseEmpty), errors.Is(err, ErrPassphraseMismatch),
		errors.Is(err, ErrNoIdentity), errors.Is(err, ErrSchema), errors.Is(err, statedb.ErrLooseMode),
		errors.Is(err, fs.ErrNotExist), errors.Is(err, fs.ErrExist):
		return "refused"
	default:
		return "io"
	}
}
