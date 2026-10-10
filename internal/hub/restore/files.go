package restore

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/store"
)

// PreviousPrefix starts the name of the kept copy of a replaced state.db:
// state.db.pre-restore-<UTC stamp>, with -2, -3 and so on when two
// restores share a second.
const PreviousPrefix = statedb.FileName + ".pre-restore-"

const stampLayout = "20060102T150405Z"

// ageHeaders start an age file, binary or armored.
var ageHeaders = [][]byte{[]byte("age-encryption.org/"), []byte("-----BEGIN AGE ENCRYPTED FILE-----")}

// checkSource refuses a backup that is not a regular file, is an age file,
// or has a non-empty <backup>-wal beside it. An empty one, which a
// read-only open leaves, holds nothing.
func checkSource(path string) error {
	f, err := os.Open(path) //nolint:gosec // G304: the backup the operator named on the command line
	if err != nil {
		return fmt.Errorf("restore: open the backup: %w", err)
	}
	defer func() { _ = f.Close() }() // read only
	fi, err := f.Stat()
	if err != nil {
		return fmt.Errorf("restore: stat the backup: %w", err)
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("restore: the backup %s is not a regular file", path)
	}
	head := make([]byte, 64)
	n, err := io.ReadFull(f, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return fmt.Errorf("restore: read the backup: %w", err)
	}
	for _, h := range ageHeaders {
		if bytes.HasPrefix(head[:n], h) {
			return ErrEncrypted
		}
	}
	if fi, err := os.Stat(path + "-wal"); err == nil && fi.Size() > 0 {
		return ErrBackupWAL
	}
	return nil
}

// tempName is dir/.state.db.restore-<random hex>; the file does not exist.
func tempName(dir string) (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("restore: temp name: %w", err)
	}
	return filepath.Join(dir, statedb.RestoreTempPrefix+hex.EncodeToString(b)), nil
}

// copyBackup copies src into dst, created with O_EXCL and mode 0600 so it
// is never wider than the store it becomes.
func copyBackup(src, dst string) (err error) {
	in, err := os.Open(src) //nolint:gosec // G304: the backup the operator named on the command line
	if err != nil {
		return fmt.Errorf("restore: open the backup: %w", err)
	}
	defer func() { _ = in.Close() }()                                      // read only
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // G304: a fixed prefix plus random hex in the state folder
	if err != nil {
		return fmt.Errorf("restore: create the copy: %w", err)
	}
	defer func() {
		if cerr := out.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("restore: close the copy: %w", cerr)
		}
	}()
	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("restore: copy the backup: %w", err)
	}
	return nil
}

// seal checks the closed copy has no WAL left and syncs it.
func seal(path string) error {
	if fi, err := os.Stat(path + "-wal"); err == nil && fi.Size() > 0 {
		return errors.New("restore: the copy's WAL was not folded in on close")
	}
	f, err := os.Open(path) //nolint:gosec // G304: the temporary copy in the state folder
	if err != nil {
		return fmt.Errorf("restore: sync the copy: %w", err)
	}
	return errors.Join(f.Sync(), f.Close())
}

// sameFleet refuses a backup whose fleet id is not the current state.db's.
// A state folder with no state.db (a hub moved to a new host) has nothing
// to compare.
func sameFleet(ctx context.Context, copyPath, current string) error {
	restored, err := fleetID(ctx, copyPath)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(current); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	have, err := fleetID(ctx, current)
	if err != nil {
		return errors.Join(ErrOldStore, err)
	}
	if have != restored {
		return ErrForeignFleet
	}
	return nil
}

func fleetID(ctx context.Context, path string) (string, error) {
	db, err := store.OpenReadOnly(path)
	if err != nil {
		return "", fmt.Errorf("restore: %w", err)
	}
	defer func() { _ = db.Close() }() // read only
	var id string
	if err := db.QueryRowContext(ctx, "SELECT fleet_id FROM hub_identity WHERE id = 1").Scan(&id); err != nil {
		return "", fmt.Errorf("restore: read the fleet id: %w", err)
	}
	return id, nil
}

// checkpointDSN opens the current state.db read-write without creating it,
// with the store's durability pragmas, so the checkpoint's writes reach the
// disk as a commit's would.
func checkpointDSN(path string) string {
	return (&url.URL{Scheme: "file", Path: path}).String() + "?mode=rw&_pragma=busy_timeout(5000)" +
		"&_pragma=synchronous(FULL)&_pragma=fullfsync(1)&_pragma=checkpoint_fullfsync(1)"
}

// checkpoint folds the WAL of the current state.db into its main file and
// closes it, so the file alone is the whole old store and no old WAL is
// left to replay onto the restored one. It reports whether state.db
// exists. A WAL with no state.db, or one that stays after the checkpoint,
// is [ErrOldStore]. A blocked checkpoint (another process holds a read
// transaction) or a state.db-shm that stays after the close (another
// process has the file open) is [ErrStoreInUse].
func checkpoint(ctx context.Context, path string) (bool, error) {
	if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
		return false, walLeft(path)
	}
	db, err := sql.Open("sqlite", checkpointDSN(path))
	if err != nil {
		return true, errors.Join(ErrOldStore, err)
	}
	var busy, frames, done int
	err = db.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &frames, &done)
	if err := errors.Join(err, db.Close()); err != nil {
		return true, errors.Join(ErrOldStore, err)
	}
	if busy != 0 {
		return true, ErrStoreInUse
	}
	// A checkpoint that was not blocked leaves no WAL, whoever else has the
	// file open, so one left now means the store itself is wrong.
	if err := walLeft(path); err != nil {
		return true, err
	}
	if _, err := os.Lstat(path + "-shm"); err == nil {
		return true, ErrStoreInUse
	}
	return true, nil
}

func walLeft(path string) error {
	if fi, err := os.Stat(path + "-wal"); err == nil && fi.Size() > 0 {
		return fmt.Errorf("%w (state.db-wal holds %d bytes)", ErrOldStore, fi.Size())
	}
	return nil
}

// keep links the checkpointed state.db to state.db.pre-restore-<stamp> in
// the same folder (same inode), makes it mode 0600 whatever the old file
// had, and syncs the folder.
func keep(dir string, at time.Time) (string, error) {
	base := filepath.Join(dir, PreviousPrefix+at.UTC().Format(stampLayout))
	name := base
	for i := 2; ; i++ {
		err := os.Link(statedb.Path(dir), name)
		if err == nil {
			break
		}
		if !errors.Is(err, fs.ErrExist) || i > 100 {
			return "", fmt.Errorf("restore: keep the previous state.db: %w", err)
		}
		name = base + "-" + strconv.Itoa(i)
	}
	// name is a second link to the live state.db, so this also sets state.db
	// to 0600 until the swap: harmless, as fleetd run wants that mode anyway.
	if err := os.Chmod(name, 0o600); err != nil {
		return name, fmt.Errorf("restore: chmod the previous state.db: %w", err)
	}
	return name, syncDir(dir)
}

// swap renames the finished copy over state.db: the one atomic step.
func swap(tmp, dst string) error {
	if err := os.Rename(tmp, dst); err != nil {
		return fmt.Errorf("restore: replace state.db: %w", err)
	}
	return nil
}

// removeAll removes the temporary copy and its SQLite side files; after the
// swap the copy's name is gone and nothing is left to remove.
func removeAll(path string) error {
	var errs []error
	for _, p := range []string{path, path + "-wal", path + "-shm", path + "-journal"} {
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, fmt.Errorf("restore: remove temporary file: %w", err))
		}
	}
	return errors.Join(errs...)
}

func syncDir(dir string) error {
	d, err := os.Open(dir) //nolint:gosec // G304: the operator's state folder, opened only to fsync it
	if err != nil {
		return fmt.Errorf("restore: sync state folder: %w", err)
	}
	return errors.Join(d.Sync(), d.Close())
}
