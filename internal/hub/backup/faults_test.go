package backup_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/backup"
)

// failRemoving is a Remove seam that fails for base names with prefix and
// removes everything else.
func failRemoving(prefix string) func(string) error {
	return func(path string) error {
		if strings.HasPrefix(filepath.Base(path), prefix) {
			return &os.PathError{Op: "remove", Path: path, Err: errors.New("injected")}
		}
		return os.Remove(path)
	}
}

// A failed encryption publishes nothing and leaves no plaintext.
func TestAFailedEncryptionPublishesNothing(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	_, err := backup.Take(context.Background(), backup.Options{
		StateDir: f.state, Trigger: backup.Manual, Now: func() time.Time { return stamp }, Telemetry: f.tel,
		Terminal: &fakeTerminal{terminal: true, answers: []string{"pw", "pw"}}, WorkFactor: testWorkFactor,
	}.WithFaults(backup.Faults{Encrypt: errors.New("injected")}))
	if err == nil {
		t.Fatal("Take succeeded")
	}
	ev := f.logs.events(t, "backup.failed")
	if len(ev) != 1 || ev[0]["step"] != "encrypt" || ev[0]["err_kind"] != "io" {
		t.Errorf("backup.failed lines = %v, want one with step encrypt, err_kind io", ev)
	}
	if got := names(t, f.backups()); len(got) != 0 {
		t.Errorf("backups/ holds %q after a failed encryption", got)
	}
	if left := plaintextLeft(t, f.state); len(left) != 0 {
		t.Errorf("the state folder holds %q: the plaintext intermediate was not removed", left)
	}
}

// A temporary file that cannot be removed after the copy was published is
// step cleanup: the copy stays published and its Result comes back with the
// error, so fleetd backup can still print the path.
// A folder sync that fails after the link is step publish, but the copy
// exists under its final name, so its Result comes back with the error and
// no temporary name is left.
func TestAFailedSyncAfterTheLinkReturnsThePublishedCopy(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	res, err := backup.Take(context.Background(), backup.Options{
		StateDir: f.state, Trigger: backup.Manual, Now: func() time.Time { return stamp }, Telemetry: f.tel,
	}.WithFaults(backup.Faults{PublishSync: errors.New("sync failed")}))
	if err == nil {
		t.Fatal("Take succeeded although the folder sync failed")
	}
	want := filepath.Join(f.backups(), "manual-20261008T030000Z.db")
	if res.Path != want || res.Bytes == 0 {
		t.Errorf("result %+v, want the published path %s with its size", res, want)
	}
	assertBackupStore(t, want)
	entries, err := os.ReadDir(f.backups())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("backups/ holds %d entries, want only the published copy", len(entries))
	}
	ev := f.logs.events(t, "backup.failed")
	if len(ev) != 1 || ev[0]["step"] != "publish" {
		t.Errorf("backup.failed lines = %v, want one with step publish", ev)
	}
}

func TestACleanupFailureKeepsThePublishedCopy(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	res, err := backup.Take(context.Background(), backup.Options{
		StateDir: f.state, Trigger: backup.Manual, Now: func() time.Time { return stamp }, Telemetry: f.tel,
		Terminal: &fakeTerminal{terminal: true, answers: []string{"pw", "pw"}}, WorkFactor: testWorkFactor,
	}.WithFaults(backup.Faults{Remove: failRemoving(".backup-plain-")}))
	if err == nil {
		t.Fatal("Take succeeded although the plaintext could not be removed")
	}
	want := filepath.Join(f.backups(), "manual-20261008T030000Z.db.age")
	if res.Path != want || !res.Encrypted {
		t.Errorf("result %+v, want the published path %s", res, want)
	}
	assertBackupStore(t, decrypt(t, want, "pw"))
	ev := f.logs.events(t, "backup.failed")
	if len(ev) != 1 || ev[0]["step"] != "cleanup" || ev[0]["err_kind"] != "io" {
		t.Errorf("backup.failed lines = %v, want one with step cleanup, err_kind io", ev)
	}
}

// A nightly copy whose cleanup fails is still published, so the job still
// prunes; old copies that cannot be removed are step prune.
func TestTheNightlyJobPrunesAfterACleanupFailureAndReportsAPruneFailure(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, prefix, step string
		left               int // nightly copies afterwards
	}{
		{name: "cleanup fails", prefix: ".partial-", step: "cleanup", left: backup.Keep},
		{name: "prune fails", prefix: "nightly-", step: "prune", left: backup.Keep + 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			for d := 1; d <= backup.Keep; d++ {
				f.touch(t, nightlyName(local(d, 3, 0)))
			}
			c := &clock{}
			s, err := backup.NewSchedule(backup.ScheduleOptions{StateDir: f.state, Now: c.Now, Telemetry: f.tel}.
				WithFaults(backup.Faults{Remove: failRemoving(tc.prefix)}))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close() })
			check(s, c, local(8, 3, 0))
			got := f.nightlies(t)
			if len(got) != tc.left || got[len(got)-1] != nightlyName(local(8, 3, 0)) {
				t.Errorf("nightly copies %q, want %d ending with the 03:00 copy", got, tc.left)
			}
			ev := f.logs.events(t, "backup.failed")
			if len(ev) != 1 || ev[0]["trigger"] != "nightly" || ev[0]["step"] != tc.step || ev[0]["err_kind"] != "io" {
				t.Errorf("backup.failed lines = %v, want one nightly with step %s, err_kind io", ev, tc.step)
			}
		})
	}
}
