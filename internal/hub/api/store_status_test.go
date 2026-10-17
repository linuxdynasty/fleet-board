package api_test

import (
	"context"
	"crypto/rand"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"connectrpc.com/connect"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/buildinfo"
	"github.com/linuxdynasty/fleet-board/internal/hub/api"
	"github.com/linuxdynasty/fleet-board/internal/hub/secret"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// withStoreSecret ensures the approval secret and sets its header on a
// GetStoreStatus request: the read carries the ADMIN scope, so it needs
// the secret in local_trusted mode (ADR 0032).
func withStoreSecret(t *testing.T, e *fleetEnv, req *connect.Request[hubv1.GetStoreStatusRequest],
) *connect.Request[hubv1.GetStoreStatusRequest] {
	t.Helper()
	secretMu.Lock()
	defer secretMu.Unlock()
	if _, err := secret.Ensure(e.dir, rand.Read); err != nil {
		t.Fatal(err)
	}
	shown, err := secret.Reveal(e.dir)
	if err != nil {
		t.Fatal(err)
	}
	req.Header().Set(api.SecretHeader, shown)
	return req
}

// TestGetStoreStatusServesStateDbStatus is 02k's store-status read: the
// hub's own storage in one message. state.db reports its file size and
// migrated schema version; the hub reports its build and start.
func TestGetStoreStatusServesStateDbStatus(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	resp, err := e.client.GetStoreStatus(context.Background(),
		withStoreSecret(t, e, connect.NewRequest(&hubv1.GetStoreStatusRequest{})))
	if err != nil {
		t.Fatalf("GetStoreStatus: %v", err)
	}
	st := resp.Msg.GetStatus()
	if st == nil {
		t.Fatal("no status in the response")
	}
	if len(st.GetDatabases()) != 1 {
		t.Fatalf("%d databases, want exactly state.db (logs.db arrives with row 25)", len(st.GetDatabases()))
	}
	db := st.GetDatabases()[0]
	if db.GetName() != "state.db" {
		t.Errorf("database %q, want state.db", db.GetName())
	}
	fi, err := os.Stat(statedb.Path(e.dir))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := db.GetSizeBytes(), strconv.FormatInt(fi.Size(), 10); strconv.FormatUint(got, 10) != want {
		t.Errorf("size_bytes %d, want the state.db file size %s", got, want)
	}
	if db.GetSchemaVersion() == 0 {
		t.Error("schema_version is 0, want the migrated version the store opened with")
	}
	if db.GetLastCheckpointTime() != nil {
		t.Error("last_checkpoint_time is set, want unset: the store records no per-file checkpoint time")
	}
	if st.GetBackup() != nil {
		t.Error("backup is set, want unset with no backup in backups/")
	}
	if st.GetStateFreeDiskBytes() == 0 {
		t.Error("state_free_disk_bytes is 0, want the free bytes on the state volume")
	}
	if got := st.GetHubStartTime().AsTime(); !got.Equal(fleetNow) {
		t.Errorf("hub_start_time %v, want the hub clock %v", got, fleetNow)
	}
	if st.GetHubVersion() != buildinfo.Version {
		t.Errorf("hub_version %q, want buildinfo.Version %q", st.GetHubVersion(), buildinfo.Version)
	}
}

// TestGetStoreStatusServesTheNewestBackup covers the backup half of the
// status: the newest published copy by the stamp in its name, with its
// size, its clear-or-encrypted kind and its path. Stamps and sizes are
// worked out by hand from the files the test writes.
func TestGetStoreStatusServesTheNewestBackup(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		// files written into backups/ before the call.
		files map[string]string
		// wantStamp is the served backup time; wantBytes its size;
		// wantClear whether it holds secrets in the clear.
		wantStamp time.Time
		wantBytes uint64
		wantClear bool
		wantFile  string
	}{
		{
			name:      "plain manual copy",
			files:     map[string]string{"manual-20261006T030000Z.db": "backup-bytes"},
			wantStamp: time.Date(2026, 10, 6, 3, 0, 0, 0, time.UTC),
			wantBytes: uint64(len("backup-bytes")),
			wantClear: true,
			wantFile:  "manual-20261006T030000Z.db",
		},
		{
			name: "newest of several wins over older and non-backups",
			files: map[string]string{
				"nightly-20261005T030000Z.db": "old",
				"manual-20261006T030000Z.db":  "new",
				"notes.txt":                   "not a backup",
				".partial-abc":                "a killed copy",
			},
			wantStamp: time.Date(2026, 10, 6, 3, 0, 0, 0, time.UTC),
			wantBytes: uint64(len("new")),
			wantClear: true,
			wantFile:  "manual-20261006T030000Z.db",
		},
		{
			name:      "encrypted copy is not in the clear",
			files:     map[string]string{"manual-20261006T030000Z.db.age": "sealed"},
			wantStamp: time.Date(2026, 10, 6, 3, 0, 0, 0, time.UTC),
			wantBytes: uint64(len("sealed")),
			wantClear: false,
			wantFile:  "manual-20261006T030000Z.db.age",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newFleetEnv(t, localUser("alice"))
			dir := filepath.Join(e.dir, "backups")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			for name, body := range tc.files {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			resp, err := e.client.GetStoreStatus(context.Background(),
				withStoreSecret(t, e, connect.NewRequest(&hubv1.GetStoreStatusRequest{})))
			if err != nil {
				t.Fatalf("GetStoreStatus: %v", err)
			}
			got := resp.Msg.GetStatus().GetBackup()
			if got == nil {
				t.Fatal("no backup in the status, want the newest copy")
			}
			if at := got.GetLastBackupTime().AsTime(); !at.Equal(tc.wantStamp) {
				t.Errorf("last_backup_time %v, want the stamp %v", at, tc.wantStamp)
			}
			if got.GetLastBackupBytes() != tc.wantBytes {
				t.Errorf("last_backup_bytes %d, want %d", got.GetLastBackupBytes(), tc.wantBytes)
			}
			if got.GetLastBackupUnencrypted() != tc.wantClear {
				t.Errorf("last_backup_unencrypted %v, want %v", got.GetLastBackupUnencrypted(), tc.wantClear)
			}
			if got.GetLastBackupIncludesLogs() {
				t.Error("last_backup_includes_logs is true, want false: logs.db is not backed up until row 25")
			}
			if want := filepath.Join(dir, tc.wantFile); got.GetLastBackupPath() != want {
				t.Errorf("last_backup_path %q, want %q", got.GetLastBackupPath(), want)
			}
		})
	}
}

// TestStoreStatusNeedsAnAdminCaller pins the ADMIN scope (02d): nobody
// without a caller identity, and no non-local caller, reads the store
// status.
func TestStoreStatusNeedsAnAdminCaller(t *testing.T) {
	t.Parallel()
	anon := newFleetEnv(t, nil)
	if _, err := anon.client.GetStoreStatus(context.Background(),
		connect.NewRequest(&hubv1.GetStoreStatusRequest{})); err == nil {
		t.Error("GetStoreStatus without a caller succeeded, want UNAUTHENTICATED")
	} else if code, _, _ := detail(t, err); code != connect.CodeUnauthenticated {
		t.Errorf("no caller: code %s, want UNAUTHENTICATED", code)
	}

	remote := &typev1.Principal{
		Name: "bot", Role: typev1.Role_ROLE_VIEWER,
		Kind: typev1.PrincipalKind_PRINCIPAL_KIND_API_TOKEN,
		Id:   &typev1.PrincipalId{Value: "token:1"},
	}
	wrong := newFleetEnv(t, remote)
	if _, err := wrong.client.GetStoreStatus(context.Background(),
		connect.NewRequest(&hubv1.GetStoreStatusRequest{})); err == nil {
		t.Error("GetStoreStatus for a non-local caller succeeded, want PERMISSION_DENIED")
	} else if code, reason, _ := detail(t, err); code != connect.CodePermissionDenied ||
		reason != typev1.ReasonCode_REASON_CODE_SCOPE_MISSING {
		t.Errorf("non-local caller: code %s reason %s, want PERMISSION_DENIED/SCOPE_MISSING", code, reason)
	}
}

// TestStoreStatusNeedsTheLocalSecret pins ADR 0032 for the read: in
// local_trusted mode the ADMIN call fails closed without the approval
// secret, and passes with it or outside local_trusted mode.
func TestStoreStatusNeedsTheLocalSecret(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	if _, err := e.client.GetStoreStatus(context.Background(),
		connect.NewRequest(&hubv1.GetStoreStatusRequest{})); err == nil {
		t.Error("GetStoreStatus without the secret succeeded, want PERMISSION_DENIED")
	} else if code, reason, _ := detail(t, err); code != connect.CodePermissionDenied ||
		reason != typev1.ReasonCode_REASON_CODE_CREDENTIALS_INVALID {
		t.Errorf("no secret: code %s reason %s, want PERMISSION_DENIED/CREDENTIALS_INVALID", code, reason)
	}
	if _, err := e.client.GetStoreStatus(context.Background(),
		withStoreSecret(t, e, connect.NewRequest(&hubv1.GetStoreStatusRequest{}))); err != nil {
		t.Errorf("GetStoreStatus with the secret: %v, want success", err)
	}

	open := newFleetEnv(t, localUser("alice"), trusted(false))
	if _, err := open.client.GetStoreStatus(context.Background(),
		connect.NewRequest(&hubv1.GetStoreStatusRequest{})); err != nil {
		t.Errorf("GetStoreStatus outside local_trusted without the secret: %v, want success", err)
	}
}

// TestStoreStatusWithoutAStateDirIsTheHubsFailure pins the INTERNAL path:
// a hub whose state dir holds no state.db cannot answer about it.
func TestStoreStatusWithoutAStateDirIsTheHubsFailure(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	dir := t.TempDir()
	fleet, err := api.NewFleet(api.FleetOptions{
		Store: e.st, CACert: e.caDER, Telemetry: e.tel, Now: func() time.Time { return fleetNow },
		StateDir: dir, LocalTrusted: true, HubInstanceID: testInstanceID,
	})
	if err != nil {
		t.Fatal(err)
	}
	secretMu.Lock()
	if _, err := secret.Ensure(dir, rand.Read); err != nil {
		secretMu.Unlock()
		t.Fatal(err)
	}
	shown, err := secret.Reveal(dir)
	secretMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	ctx := api.WithPrincipal(context.Background(), localUser("alice"))
	req := connect.NewRequest(&hubv1.GetStoreStatusRequest{})
	req.Header().Set(api.SecretHeader, shown)
	if _, err := fleet.GetStoreStatus(ctx, req); err == nil {
		t.Error("GetStoreStatus with no state.db succeeded, want INTERNAL")
	} else if code, reason, _ := detail(t, err); code != connect.CodeInternal ||
		reason != typev1.ReasonCode_REASON_CODE_INTERNAL {
		t.Errorf("no state.db: code %s reason %s, want INTERNAL/INTERNAL", code, reason)
	}
}
