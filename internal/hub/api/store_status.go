package api

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/buildinfo"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// backupName is a published backup in backups/: the fileName of
// internal/hub/backup (`nightly-<UTC stamp>.db` from fleetd run,
// `manual-<UTC stamp>.db` or `manual-<UTC stamp>.db.age` from fleetd
// backup --encrypt), which is also what system.BackupAge reads. Anything
// else (temp files, notes, a bad stamp) is not a backup. A nightly name
// ending in .age is not one the hub made, so it never counts.
var backupName = regexp.MustCompile(`^(nightly|manual)-([0-9]{8}T[0-9]{6}Z)\.db(\.age)?$`)

// backupStamp is the UTC time layout of backup file names.
const backupStamp = "20060102T150405Z"

// GetStoreStatus reads the hub's own storage in one message (02k): the
// state.db file with its size and migrated schema version, the newest
// published backup, the free bytes on the state volume, and the hub's
// start and build. It needs ADMIN (02d), so on the admin socket only
// the local user holds it, and in local_trusted mode the local approval
// secret as well (ADR 0032).
//
// Served as far as row 12 reaches: logs.db has no entry until row 25
// brings the file, last_checkpoint_time stays unset (the store records
// no per-file checkpoint time), and last_backup_includes_logs stays
// false (fleetd backup has no --include-logs yet). A state dir with no
// state.db, an unreadable backups/ folder, or a volume whose free
// space cannot be read is the hub's failure, INTERNAL: an unknown free
// space reading stays unknown, it is never served as 0 (a full disk).
func (f *Fleet) GetStoreStatus(ctx context.Context, req *connect.Request[hubv1.GetStoreStatusRequest],
) (*connect.Response[hubv1.GetStoreStatusResponse], error) {
	who, err := authorizeAdmin(ctx)
	if err != nil {
		return nil, f.storeRefused(ctx, err)
	}
	actor := who.GetId().GetValue()
	// ADMIN scope, so the local approval secret is needed in local_trusted
	// mode too (ADR 0032), like every other ADMIN call in RequiresLocalSecret.
	if err := f.checkLocalSecret(hubProcedure("GetStoreStatus"), req.Header().Get(SecretHeader)); err != nil {
		return nil, f.storeRefused(ctx, apiError(connect.CodePermissionDenied,
			typev1.ReasonCode_REASON_CODE_CREDENTIALS_INVALID,
			"this call needs the local approval secret", nil, nil))
	}
	if f.stateDir == "" {
		return nil, f.storeFailed(ctx, actor, "stat", errors.New("api: no state dir to read the store status from"))
	}
	fi, err := os.Stat(statedb.Path(f.stateDir))
	if err != nil {
		return nil, f.storeFailed(ctx, actor, "stat", err)
	}
	backup, err := newestBackup(f.stateDir)
	if err != nil {
		return nil, f.storeFailed(ctx, actor, "backup", err)
	}
	freeBytes, ok := f.diskFree(f.stateDir)
	if !ok {
		return nil, f.storeFailed(ctx, actor, "disk", errors.New("api: the free space on the state volume is unknown"))
	}
	if err := quotaCheckpoint(ctx, quotaRead); err != nil {
		return nil, err
	}
	return connect.NewResponse(&hubv1.GetStoreStatusResponse{Status: &hubv1.StoreStatus{
		Databases: []*hubv1.DatabaseStatus{{
			Name:          statedb.FileName,
			SizeBytes:     uint64(fi.Size()),               //nolint:gosec // G115: a stat size is never negative
			SchemaVersion: uint32(f.store.SchemaVersion()), //nolint:gosec // G115: schema versions are small positive integers
		}},
		Backup:             backup,
		StateFreeDiskBytes: uint64(freeBytes), //nolint:gosec // G115: DiskFree returns 0..MaxInt64
		HubStartTime:       timestamppb.New(f.started),
		HubVersion:         buildinfo.Version,
	}}), nil
}

// newestBackup is the newest published backup in the state folder's
// backups/ folder, oldest names lose. It returns nil while the folder
// holds none (a missing folder holds none). The time is the stamp in
// the file name, the same clock BACKUP_AGE reads; the bytes are the
// file's size; unencrypted is false for a .age copy; includes-logs is
// always false until row 25 brings logs.db. The path is the copy's
// path on the hub host (untrusted text, but the name pattern above
// bounds its charset).
func newestBackup(stateDir string) (*hubv1.BackupStatus, error) {
	dir := filepath.Join(stateDir, "backups")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var (
		name string
		at   time.Time
	)
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		m := backupName.FindStringSubmatch(e.Name())
		if m == nil || (m[1] == "nightly" && m[3] != "") {
			continue
		}
		stamp, err := time.Parse(backupStamp, m[2])
		if err != nil {
			continue
		}
		if stamp.After(at) {
			name, at = e.Name(), stamp
		}
	}
	if name == "" {
		return nil, nil
	}
	fi, err := os.Stat(filepath.Join(dir, name))
	if err != nil {
		return nil, err
	}
	return &hubv1.BackupStatus{
		LastBackupTime:        timestamppb.New(at),
		LastBackupBytes:       uint64(fi.Size()), //nolint:gosec // G115: a stat size is never negative
		LastBackupUnencrypted: !strings.HasSuffix(name, ".age"),
		LastBackupPath:        filepath.Join(dir, name),
	}, nil
}

// storeRefused logs a store-status caller error by its reason and returns
// it. The read adds no counter: only the writes count outcomes.
func (f *Fleet) storeRefused(ctx context.Context, err error) error {
	f.tel.Logger.InfoContext(ctx, "store.issue_refused", "reason", reasonOf(err))
	return err
}

// storeFailed logs the hub's own store-status failure and returns
// INTERNAL without the cause's text.
func (f *Fleet) storeFailed(ctx context.Context, actor, step string, cause error) error {
	kind := "io"
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		kind = "timeout"
	}
	f.tel.Logger.ErrorContext(ctx, "store.failed", "step", step, "err_kind", kind, "actor", actor)
	return apiError(connect.CodeInternal, typev1.ReasonCode_REASON_CODE_INTERNAL,
		"the hub could not read its store status", nil, nil)
}
