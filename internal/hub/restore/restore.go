package restore

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"slices"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/backup"
	"github.com/linuxdynasty/fleet-board/internal/hub/ca"
	"github.com/linuxdynasty/fleet-board/internal/hub/revocation"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/store"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

// ReconcileWindow is how long after a restore the hub adopts what agents
// know and it does not (02g, 03f).
const ReconcileWindow = 24 * time.Hour

// Why a restore was refused.
var (
	// ErrEncrypted means the backup is an age file; restore reads only a
	// plaintext copy of state.db.
	ErrEncrypted = errors.New("restore: the backup is encrypted with age; decrypt it first (age -d -o <file> <backup>), keep the plaintext private, and restore that")
	// ErrForeignFleet means the backup's fleet id is not the one in the
	// state.db it would replace.
	ErrForeignFleet = errors.New("restore: the backup belongs to another fleet than this state.db; move state.db out of the state folder first if you mean to replace that fleet")
	// ErrOldStore means the state.db to be replaced could not be read or
	// checkpointed, or a state.db-wal sits there without it, so it cannot
	// be kept whole.
	ErrOldStore = errors.New("restore: the current state.db could not be read or checkpointed; move state.db, state.db-wal and state.db-shm out of the state folder, then restore again")
	// ErrStoreInUse means another process has the current state.db open
	// (fleetd backup takes no lock): its checkpoint was blocked, or
	// state.db-shm stayed after the checkpoint closed. Swapping the file
	// under it would leave that process's shared memory on the new store.
	ErrStoreInUse = errors.New("restore: another process has state.db open (a fleetd backup?); wait for it to finish and restore again")
	// ErrBackupWAL means the backup has a non-empty <backup>-wal beside it,
	// whose committed rows a copy of the main file would lose.
	ErrBackupWAL = errors.New("restore: the backup has a non-empty -wal file beside it; fold it in first (sqlite3 <backup> 'PRAGMA wal_checkpoint(TRUNCATE)') or restore a copy fleetd backup made")
)

// Options configure [Run]. The caller holds fleetd.lock for the whole call.
type Options struct {
	// StateDir is the state folder; state.db in it is replaced.
	StateDir string
	// Backup is the backup file (a plaintext copy of state.db).
	Backup string
	// Address, when set, is a hub address the owner typed (host or
	// host:port, see [Address]): it is added to hub_addresses and the
	// server certificate is re-issued to cover its host.
	Address string
	// DefaultPort is the port of an Address given without one.
	DefaultPort string
	// Names is the server certificate's name list for the given hub
	// addresses, the same list fleetd run wants. Needed with Address.
	Names func(hubAddresses []string) ([]string, error)
	// Now is the clock; nil means time.Now.
	Now func() time.Time
	// Telemetry receives restore.completed and restore.failed; nil turns
	// them off.
	Telemetry *telemetry.Telemetry

	// stepHook, when set, runs after each step; tests stop the process there.
	stepHook func(step string)
	// syncErr, when set, is the error of the folder sync after the swap.
	syncErr error
}

// Result is a finished restore.
type Result struct {
	// Previous is the kept copy of the replaced state.db; empty when the
	// state folder had none.
	Previous string
	// Replaced is true once the new store was renamed over state.db, even
	// when a later step (the folder sync, the cleanup) returns an error.
	Replaced bool
	// ReconcileUntil ends the reconcile window.
	ReconcileUntil time.Time
}

// step is the closed step field of restore.failed.
type step string

const (
	stepAddress     step = "address"
	stepSource      step = "source"
	stepCopy        step = "copy"
	stepVerify      step = "verify"
	stepFleet       step = "fleet"
	stepRewrite     step = "rewrite"
	stepCheckpoint  step = "checkpoint"
	stepKeep        step = "keep"
	stepSwap        step = "swap"
	stepSync        step = "sync"
	stepCleanupTemp step = "cleanup"
)

// Run replaces state.db with the backup, offline, so that a crash at any
// point leaves either the old store or the new one whole. It spells
// --address the way agents keep it ([Address]), copies the backup to a
// temporary file in the state folder (mode 0600), checks it
// (backup.Verify), refuses a backup from another fleet than the current
// state.db, migrates the copy forward, and rewrites its hub_identity row in
// one transaction: a new settings_epoch, the address (and a server
// certificate from the same CA covering it), and the reconcile window. The
// revoked-serials file is applied to the copy. Then the current state.db
// is checkpointed (its WAL folded in), kept as
// state.db.pre-restore-<UTC stamp> (a hard link, same inode), and the copy
// renamed over state.db. Success logs restore.completed; a failure logs
// restore.failed with its step and removes the temporary file.
func Run(ctx context.Context, opts Options) (Result, error) {
	log := slog.New(slog.DiscardHandler)
	if opts.Telemetry != nil {
		log = opts.Telemetry.Logger.With("component", "restore")
	}
	res, st, err := run(ctx, opts)
	if err != nil {
		log.Error("restore.failed", "step", string(st), "err_kind", errKind(err))
		return res, err
	}
	log.Info("restore.completed", "address_added", opts.Address != "", "previous_kept", res.Previous != "",
		"reconcile_until", res.ReconcileUntil.UTC().Format(time.RFC3339))
	return res, nil
}

func run(ctx context.Context, o Options) (res Result, st step, err error) {
	now := time.Now
	if o.Now != nil {
		now = o.Now
	}
	at := now()
	hook := func(s step) {
		if o.stepHook != nil {
			o.stepHook(string(s))
		}
	}
	if o.Address != "" {
		if o.Address, err = Address(o.Address, o.DefaultPort); err != nil {
			return res, stepAddress, err
		}
	}
	if err := checkSource(o.Backup); err != nil {
		return res, stepSource, err
	}
	tmp, err := tempName(o.StateDir)
	if err != nil {
		return res, stepCopy, err
	}
	defer func() {
		if rmErr := removeAll(tmp); rmErr != nil && err == nil {
			st, err = stepCleanupTemp, rmErr
		}
	}()
	if err := copyBackup(o.Backup, tmp); err != nil {
		return res, stepCopy, err
	}
	hook(stepCopy)
	if err := backup.Verify(ctx, tmp); err != nil {
		return res, stepVerify, fmt.Errorf("restore: %s is not a usable backup: %w", o.Backup, err)
	}
	if err := sameFleet(ctx, tmp, statedb.Path(o.StateDir)); err != nil {
		return res, stepFleet, err
	}
	res.ReconcileUntil = at.Add(ReconcileWindow)
	if err := rewrite(ctx, tmp, o, at, res.ReconcileUntil); err != nil {
		return res, stepRewrite, err
	}
	if err := seal(tmp); err != nil {
		return res, stepRewrite, err
	}
	hook(stepRewrite)
	old, err := checkpoint(ctx, statedb.Path(o.StateDir))
	if err != nil {
		return res, stepCheckpoint, err
	}
	hook(stepCheckpoint)
	if old {
		if res.Previous, err = keep(o.StateDir, at); err != nil {
			return res, stepKeep, err
		}
	}
	hook(stepKeep)
	if err := swap(tmp, statedb.Path(o.StateDir)); err != nil {
		return res, stepSwap, err
	}
	res.Replaced = true
	hook(stepSwap)
	err = o.syncErr
	if err == nil {
		err = syncDir(o.StateDir)
	}
	if err != nil {
		return res, stepSync, err
	}
	return res, "", nil
}

// rewrite migrates the copy at path forward and writes the restore's
// identity changes and the revocation list into it, then closes it, which
// folds its WAL into the file.
func rewrite(ctx context.Context, path string, o Options, at, until time.Time) (err error) {
	s, err := store.Open(ctx, store.Options{Path: path, Name: "state", Migrations: statedb.Migrations(), MustExist: true})
	if err != nil {
		return fmt.Errorf("restore: open the copy: %w", err)
	}
	defer func() { err = errors.Join(err, s.Close()) }()
	id, err := statedb.ReadIdentity(ctx, s)
	if err != nil {
		return err
	}
	u := statedb.RestoreUpdate{HubAddresses: id.HubAddresses, ReconcileUntil: until}
	if u.SettingsEpoch, err = newEpoch(id.SettingsEpoch); err != nil {
		return err
	}
	if o.Address != "" && !slices.Contains(u.HubAddresses, o.Address) {
		u.HubAddresses = append(slices.Clone(u.HubAddresses), o.Address)
	}
	if o.Address != "" {
		if err := issue(&u, id, o.Names, at); err != nil {
			return err
		}
	}
	if err := statedb.ApplyRestore(ctx, s, u); err != nil {
		return err
	}
	var log *slog.Logger
	if o.Telemetry != nil {
		log = o.Telemetry.Logger
	}
	if _, err := revocation.Load(ctx, revocation.Options{Dir: o.StateDir, Store: s, Now: func() time.Time { return at }, Logger: log}); err != nil {
		return fmt.Errorf("restore: apply revoked-serials: %w", err)
	}
	return nil
}

// issue signs a server certificate for the names fleetd run wants for the
// new address list, with the restored CA.
func issue(u *statedb.RestoreUpdate, id statedb.Identity, names func([]string) ([]string, error), at time.Time) error {
	if names == nil {
		return errors.New("restore: no server name list for --address")
	}
	list, err := names(u.HubAddresses)
	if err != nil {
		return err
	}
	auth, err := ca.Parse(id.CACert, id.CAKey)
	if err != nil {
		return fmt.Errorf("restore: the backup's CA: %w", err)
	}
	sc, err := auth.IssueServer(list, at, rand.Reader)
	if err != nil {
		return err
	}
	u.ServerCert, u.ServerKey, u.ServerNames = sc.CertDER, sc.KeyDER, sc.Names
	return nil
}

// newEpoch is a random (version 4) UUID unlike the restored epoch.
func newEpoch(old string) (string, error) {
	for {
		b := make([]byte, 16)
		if _, err := rand.Read(b); err != nil {
			return "", fmt.Errorf("restore: settings epoch: %w", err)
		}
		b[6] = b[6]&0x0f | 0x40
		b[8] = b[8]&0x3f | 0x80
		if e := fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]); e != old {
			return e, nil
		}
	}
}

// errKind is the closed err_kind class of 05b.
func errKind(err error) string {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded), errors.Is(err, ErrStoreInUse):
		return "timeout"
	case errors.Is(err, ErrAddress), errors.Is(err, ErrEncrypted), errors.Is(err, ErrBackupWAL), errors.Is(err, ErrForeignFleet),
		errors.Is(err, ErrOldStore),
		errors.Is(err, backup.ErrCorrupt), errors.Is(err, backup.ErrSchema), errors.Is(err, backup.ErrNoIdentity),
		errors.Is(err, store.ErrSchemaTooNew), errors.Is(err, store.ErrMigrationChanged), errors.Is(err, revocation.ErrInvalid),
		errors.Is(err, statedb.ErrLooseMode), errors.Is(err, fs.ErrNotExist):
		return "refused"
	default:
		return "io"
	}
}
