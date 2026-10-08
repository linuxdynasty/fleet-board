package backup_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/backup"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

func (f *fixture) take(t *testing.T, trigger backup.Trigger, term backup.Terminal) (backup.Result, error) {
	t.Helper()
	return backup.Take(context.Background(), backup.Options{
		StateDir: f.state, Trigger: trigger, Terminal: term,
		Now: func() time.Time { return stamp }, Telemetry: f.tel, WorkFactor: testWorkFactor,
	})
}

// Plan row 3: a backup opens, holds hub_identity and is mode 0600, for a
// nightly and a manual copy. backups/ is created 0700 and holds only the
// published file (no temporary file is left).
func TestPlainCopiesOpenHoldTheIdentityAndArePrivate(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		trigger backup.Trigger
		name    string
	}{
		{backup.Nightly, "nightly-20261008T030000Z.db"},
		{backup.Manual, "manual-20261008T030000Z.db"},
	} {
		t.Run(string(tc.trigger), func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			res, err := f.take(t, tc.trigger, nil)
			if err != nil {
				t.Fatalf("Take: %v", err)
			}
			want := filepath.Join(f.backups(), tc.name)
			if res.Path != want || res.Encrypted {
				t.Errorf("result %+v, want path %s, not encrypted", res, want)
			}
			if got := names(t, f.backups()); len(got) != 1 || got[0] != tc.name {
				t.Errorf("backups/ holds %q, want only %s", got, tc.name)
			}
			if m := mode(t, f.backups()); m != 0o700 {
				t.Errorf("backups/ mode %04o, want 0700", m)
			}
			if m := mode(t, want); m != 0o600 {
				t.Errorf("%s mode %04o, want 0600", tc.name, m)
			}
			assertBackupStore(t, want)
			fi, err := os.Stat(want)
			if err != nil {
				t.Fatal(err)
			}
			if res.Bytes != fi.Size() {
				t.Errorf("result bytes %d, file size %d", res.Bytes, fi.Size())
			}
			ev := f.logs.events(t, "backup.completed")
			if len(ev) != 1 || ev[0]["trigger"] != string(tc.trigger) || ev[0]["encrypted"] != false ||
				ev[0]["bytes"] != float64(fi.Size()) {
				t.Errorf("backup.completed lines = %v, want one with trigger %s, bytes %d, encrypted false",
					ev, tc.trigger, fi.Size())
			}
			if left := plaintextLeft(t, f.state); len(left) != 0 {
				t.Errorf("the state folder holds %q after the backup", left)
			}
		})
	}
}

// Plan row 3: a backup taken while the store is being written opens, holds
// hub_identity and passes quick_check.
func TestACopyTakenWhileTheStoreIsBeingWritten(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	st, err := statedb.Open(context.Background(), f.state, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	var writes atomic.Int64
	first := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for ctx.Err() == nil {
			err := st.Write(ctx, func(tx *sql.Tx) error {
				_, err := tx.ExecContext(context.WithoutCancel(ctx), "UPDATE hub_identity SET hub_addresses = ? WHERE id = 1",
					`["`+time.Now().String()+`"]`)
				return err
			})
			if err != nil {
				return
			}
			if writes.Add(1) == 1 {
				close(first)
			}
		}
	}()
	select {
	case <-first:
	case <-time.After(10 * time.Second):
		cancel()
		wg.Wait()
		t.Fatal("the writer committed nothing within 10 s")
	}
	res, err := f.take(t, backup.Nightly, nil) // the writer keeps committing until cancel
	cancel()
	wg.Wait()
	if err != nil {
		t.Fatalf("Take while writing: %v", err)
	}
	assertBackupStore(t, res.Path)
	if m := mode(t, res.Path); m != 0o600 {
		t.Errorf("mode %04o, want 0600", m)
	}
}

// Every failure logs backup.failed with its step and a closed err_kind,
// publishes nothing and leaves no temporary file.
func TestFailuresLogTheStepAndPublishNothing(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, step, kind string
		setup            func(t *testing.T, f *fixture)
		empty            bool // the state folder has no state.db
	}{
		{name: "missing state.db", step: "source", kind: "refused", empty: true},
		{name: "loose state.db", step: "source", kind: "refused", setup: func(t *testing.T, f *fixture) {
			t.Helper()
			chmod(t, statedb.Path(f.state), 0o644)
		}},
		{name: "backups is a file", step: "dir", kind: "io", setup: func(t *testing.T, f *fixture) {
			t.Helper()
			if err := os.WriteFile(f.backups(), nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "loose backups folder", step: "dir", kind: "refused", setup: func(t *testing.T, f *fixture) {
			t.Helper()
			if err := os.Mkdir(f.backups(), 0o700); err != nil {
				t.Fatal(err)
			}
			chmod(t, f.backups(), 0o755)
		}},
		{name: "state.db is not a database", step: "copy", kind: "io", empty: true, setup: func(t *testing.T, f *fixture) {
			t.Helper()
			if err := os.WriteFile(statedb.Path(f.state), []byte("not sqlite, just bytes that are long enough"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "no hub_identity", step: "verify", kind: "refused", empty: true, setup: noIdentityStore},
		{name: "name taken", step: "publish", kind: "refused", setup: func(t *testing.T, f *fixture) {
			t.Helper()
			if err := os.Mkdir(f.backups(), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(f.backups(), "manual-20261008T030000Z.db"), []byte("older"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			if tc.empty {
				f = emptyFixture(t)
			}
			if tc.setup != nil {
				tc.setup(t, f)
			}
			before := names(t, f.backups())
			_, err := f.take(t, backup.Manual, nil)
			if err == nil {
				t.Fatal("Take succeeded")
			}
			ev := f.logs.events(t, "backup.failed")
			if len(ev) != 1 || ev[0]["trigger"] != "manual" || ev[0]["step"] != tc.step || ev[0]["err_kind"] != tc.kind {
				t.Errorf("backup.failed lines = %v, want one with trigger manual, step %s, err_kind %s", ev, tc.step, tc.kind)
			}
			if got := f.logs.events(t, "backup.completed"); len(got) != 0 {
				t.Errorf("backup.completed logged on failure: %v", got)
			}
			if after := names(t, f.backups()); !equal(after, before) {
				t.Errorf("backups/ changed from %q to %q", before, after)
			}
			if left := plaintextLeft(t, f.state); len(left) != 0 {
				t.Errorf("the state folder holds %q after the failure", left)
			}
		})
	}
}

// noIdentityStore writes a state.db with every migration and no
// hub_identity row: the copy must be refused.
func noIdentityStore(t *testing.T, f *fixture) {
	t.Helper()
	path := statedb.Path(f.state)
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := statedb.Open(context.Background(), f.state, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
}

func chmod(t *testing.T, path string, m os.FileMode) {
	t.Helper()
	if err := os.Chmod(path, m); err != nil {
		t.Fatal(err)
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Take honours its context: a cancelled caller gets an error and nothing is
// published.
func TestACancelledTakePublishesNothing(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := backup.Take(ctx, backup.Options{StateDir: f.state, Trigger: backup.Nightly, Telemetry: f.tel})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Take error = %v, want context.Canceled", err)
	}
	if got := names(t, f.backups()); len(got) != 0 {
		t.Errorf("backups/ holds %q after a cancelled Take", got)
	}
	ev := f.logs.events(t, "backup.failed")
	if len(ev) != 1 || ev[0]["err_kind"] != "timeout" {
		t.Errorf("backup.failed lines = %v, want one with err_kind timeout", ev)
	}
}
