package restore_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/hub/restore"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// The child process of TestARestoreKilledAtAnyStepLeavesOneWholeStore: it
// restores and SIGKILLs itself right after the step named in the
// environment, so no deferred cleanup runs.
func TestCrashChild(t *testing.T) {
	state, path, at := os.Getenv("RESTORE_CRASH_STATE"), os.Getenv("RESTORE_CRASH_BACKUP"), os.Getenv("RESTORE_CRASH_AT")
	if state == "" {
		t.Skip("runs only as the child of the crash test")
	}
	f := &fixture{state: state}
	opts := restore.WithStepHook(f.options(path), func(step string) {
		if step == at {
			_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
			select {} // the signal ends the process
		}
	})
	opts.Telemetry = nil
	opts.Address = "hub2.example:7443"
	if _, err := restore.Run(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	t.Fatalf("the restore finished without reaching step %s", at)
}

// A restore killed with SIGKILL after any step leaves state.db either the
// old store, whole (byte for byte before the checkpoint), or the new one,
// whole, never a mix and never missing. Its temporary file is a leftover
// statedb.RemoveLeftovers sweeps (fleetd init, run and restore call it).
func TestARestoreKilledAtAnyStepLeavesOneWholeStore(t *testing.T) {
	t.Parallel()
	cases := []struct {
		step     string
		restored bool // state.db is the new store
		kept     bool // the previous copy exists
		same     bool // state.db is byte for byte the old file
	}{
		{step: "copy", same: true},
		{step: "rewrite", same: true},
		{step: "checkpoint"},
		{step: "keep", kept: true},
		{step: "swap", restored: true, kept: true},
	}
	for _, tc := range cases {
		t.Run(tc.step, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t, "fleet-a")
			path := f.backupOf(t)
			execSQL(t, statedb.Path(f.state), `INSERT INTO cert_serials(serial, machine_id, not_after_time, created_time) VALUES ('dd', 'm', 1, 1)`)
			before := read(t, statedb.Path(f.state))

			child := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestCrashChild$", "-test.count=1") //nolint:gosec // G204: the test binary itself
			child.Env = append(os.Environ(), "RESTORE_CRASH_STATE="+f.state, "RESTORE_CRASH_BACKUP="+path, "RESTORE_CRASH_AT="+tc.step)
			out, err := child.CombinedOutput()
			var ee *exec.ExitError
			if !errors.As(err, &ee) || ee.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL { //nolint:forcetypeassert // unix only
				t.Fatalf("the child was not killed at %s: %v\n%s", tc.step, err, out)
			}

			if tc.same && !bytes.Equal(read(t, statedb.Path(f.state)), before) {
				t.Error("state.db changed before the checkpoint step")
			}
			if fi, err := os.Stat(statedb.Path(f.state) + "-wal"); err == nil && fi.Size() > 0 && tc.restored {
				t.Errorf("a %d-byte WAL sits next to the restored state.db", fi.Size())
			}
			got := f.identity(t)
			marker := count(t, statedb.Path(f.state), "SELECT count(*) FROM cert_serials WHERE serial = 'dd'")
			if tc.restored {
				if got.SettingsEpoch == f.id.SettingsEpoch || marker != 0 || got.RestoreReconcileUntil.IsZero() {
					t.Errorf("state.db is not the whole restored store: epoch %q, marker %d, reconcile %v", got.SettingsEpoch, marker, got.RestoreReconcileUntil)
				}
			} else if got.SettingsEpoch != f.id.SettingsEpoch || marker != 1 || !got.RestoreReconcileUntil.IsZero() {
				t.Errorf("state.db is not the whole old store: epoch %q, marker %d", got.SettingsEpoch, marker)
			}
			if n := len(restoreRows(t, statedb.Path(f.state))); (n == 1) != tc.restored || n > 1 {
				t.Errorf("%d hub.restore rows in state.db, want one only once the new store is in place", n)
			}
			prev := previous(t, f.state)
			if (len(prev) == 1) != tc.kept {
				t.Fatalf("previous copies %v, want kept = %v", prev, tc.kept)
			}
			if tc.kept && count(t, prev[0], "SELECT count(*) FROM cert_serials WHERE serial = 'dd'") != 1 {
				t.Error("the previous copy is not the old store")
			}
			if _, err := statedb.RemoveLeftovers(f.state); err != nil {
				t.Fatal(err)
			}
			if m, _ := filepath.Glob(filepath.Join(f.state, ".state.db.restore-*")); len(m) != 0 {
				t.Errorf("RemoveLeftovers left %v", m)
			}
		})
	}
}
