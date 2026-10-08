package backup_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/backup"
)

// hubZone is the hub's local time in these tests: UTC-5, so 03:00 local is
// 08:00 UTC and the slot cannot pass for UTC by accident.
var hubZone = time.FixedZone("hub", -5*60*60)

func local(day, hour, minute int) time.Time {
	return time.Date(2026, 10, day, hour, minute, 0, 0, hubZone)
}

// clock is the injected Now; tests move it between Checks.
type clock struct{ now time.Time }

func (c *clock) Now() time.Time { return c.now }

func (f *fixture) schedule(t *testing.T, c *clock) *backup.Schedule {
	t.Helper()
	s, err := backup.NewSchedule(backup.ScheduleOptions{StateDir: f.state, Now: c.Now, Telemetry: f.tel})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// touch makes an empty backup file with the given name.
func (f *fixture) touch(t *testing.T, name string) {
	t.Helper()
	if err := os.MkdirAll(f.backups(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.backups(), name), nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

func nightlyName(at time.Time) string {
	return "nightly-" + at.UTC().Format("20060102T150405Z") + ".db"
}

func manualName(at time.Time) string { return "manual-" + at.UTC().Format("20060102T150405Z") + ".db" }

func (f *fixture) nightlies(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, n := range names(t, f.backups()) {
		if strings.HasPrefix(n, "nightly-") {
			out = append(out, n)
		}
	}
	return out
}

// check moves the clock to at and runs one Check.
func check(s *backup.Schedule, c *clock, at time.Time) {
	c.now = at
	s.Check(context.Background())
}

// Plan row 3: once per local date, at the first check at or after 03:00 hub
// local time, and not again that date.
func TestTheNightlyCopyIsMadeAtTheFirstCheckAtOrAfter0300Local(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.touch(t, nightlyName(local(7, 20, 0)))
	c := &clock{}
	s := f.schedule(t, c)
	for _, at := range []time.Time{local(8, 2, 0), local(8, 2, 59)} {
		check(s, c, at)
	}
	if got := f.nightlies(t); len(got) != 1 {
		t.Fatalf("before 03:00 the folder holds %q, want only the old copy", got)
	}
	check(s, c, local(8, 3, 0))
	want := []string{nightlyName(local(7, 20, 0)), nightlyName(local(8, 3, 0))}
	if got := f.nightlies(t); !equal(got, want) {
		t.Fatalf("after the 03:00 check the folder holds %q, want %q", got, want)
	}
	assertBackupStore(t, filepath.Join(f.backups(), nightlyName(local(8, 3, 0))))
	for _, at := range []time.Time{local(8, 3, 1), local(8, 12, 0), local(8, 23, 59), local(9, 0, 0), local(9, 2, 59)} {
		check(s, c, at)
	}
	if got := f.nightlies(t); !equal(got, want) {
		t.Fatalf("later checks the same night made more: %q", got)
	}
	check(s, c, local(9, 3, 5))
	if got := f.nightlies(t); len(got) != 3 || got[2] != nightlyName(local(9, 3, 5)) {
		t.Errorf("the next date's first check after 03:00 left %q, want a copy at 03:05", got)
	}
}

// A hub that was off at 03:00 makes the date's copy at its first check that
// date after 03:00, once.
func TestAHubStartedAfter0300MakesThatDatesCopyOnce(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.touch(t, nightlyName(local(7, 20, 0)))
	c := &clock{}
	s := f.schedule(t, c)
	check(s, c, local(8, 10, 0))
	check(s, c, local(8, 10, 1))
	if got := f.nightlies(t); len(got) != 2 || got[1] != nightlyName(local(8, 10, 0)) {
		t.Errorf("the folder holds %q, want the old copy and one made at 10:00", got)
	}
}

// Plan row 3: catch up at start, or at a wake (a gap between checks), when
// the newest nightly copy is more than 24 h old.
func TestCatchUpAtStartOrWakeWhenTheNewestNightlyIsOlderThan24h(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.touch(t, nightlyName(local(6, 23, 0)))
	f.touch(t, manualName(local(7, 23, 0))) // manual copies do not count
	c := &clock{}
	s := f.schedule(t, c)
	check(s, c, local(8, 0, 30)) // start, 25.5 h after the last nightly
	if got := f.nightlies(t); len(got) != 2 || got[1] != nightlyName(local(8, 0, 30)) {
		t.Fatalf("start left %q, want a catch-up copy at 00:30", got)
	}
	check(s, c, local(8, 3, 0))  // the 8th's 03:00 copy
	check(s, c, local(10, 1, 0)) // asleep through the 9th: a wake 46 h after the last copy, before 03:00
	got := f.nightlies(t)
	if len(got) != 4 || got[3] != nightlyName(local(10, 1, 0)) {
		t.Fatalf("the wake left %q, want a catch-up copy at 01:00 on the 10th", got)
	}
}

// A failed catch-up is logged once and not tried again at every check.
func TestAFailedCatchUpIsNotRetriedAtEveryCheck(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	if err := os.Mkdir(f.backups(), 0o700); err != nil {
		t.Fatal(err)
	}
	chmod(t, f.backups(), 0o755) // loose: every Take is refused
	c := &clock{}
	s := f.schedule(t, c)
	for m := range 5 {
		check(s, c, local(8, 1, m))
	}
	ev := f.logs.events(t, "backup.failed")
	if len(ev) != 1 || ev[0]["trigger"] != "nightly" || ev[0]["step"] != "dir" {
		t.Errorf("backup.failed lines = %v, want one nightly failure at step dir", ev)
	}
}

// Plan row 3: keep the last 7 nightly copies; manual copies (plain or
// encrypted) and other files are never pruned.
func TestPruningKeepsTheLast7NightlyCopiesAndEveryManualOne(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	var old []string
	for d := 1; d <= 7; d++ {
		n := nightlyName(local(d, 3, 0))
		old = append(old, n)
		f.touch(t, n)
	}
	keep := []string{manualName(local(1, 9, 0)), manualName(local(2, 9, 0)) + ".age", "notes.txt"}
	for _, n := range keep {
		f.touch(t, n)
	}
	c := &clock{}
	s := f.schedule(t, c)
	check(s, c, local(8, 3, 0))
	want := append(old[1:], nightlyName(local(8, 3, 0)))
	if got := f.nightlies(t); !equal(got, want) {
		t.Errorf("nightly copies %q, want the newest 7: %q", got, want)
	}
	all := names(t, f.backups())
	for _, n := range keep {
		found := false
		for _, a := range all {
			found = found || a == n
		}
		if !found {
			t.Errorf("%s was removed; only nightly copies are pruned", n)
		}
	}
}

func (f *fixture) scrape(t *testing.T) string {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", nil)
	req.RemoteAddr = "127.0.0.1:1"
	rec := httptest.NewRecorder()
	f.tel.MetricsHandler(nil).ServeHTTP(rec, req)
	return rec.Body.String()
}

// fleet_hub_backup_age_seconds is BACKUP_AGE's source: the age of the newest
// backup file of either kind. With no backup it is absent (unknown), never 0.
func TestBackupAgeReadsTheNewestFileOfEitherKind(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	c := &clock{now: local(8, 12, 0)}
	f.schedule(t, c)
	if v, ok := gauge(f.scrape(t), "fleet_hub_backup_age_seconds"); ok {
		t.Errorf("with no backup the gauge reads %v, want no value", v)
	}
	f.touch(t, nightlyName(local(8, 3, 0)))
	f.touch(t, manualName(local(8, 10, 0))+".age")
	f.touch(t, "nightly-garbage.db")
	if v, ok := gauge(f.scrape(t), "fleet_hub_backup_age_seconds"); !ok || v != 7200 {
		t.Errorf("fleet_hub_backup_age_seconds = %v (present %v), want 7200: the encrypted manual copy, 2 h old", v, ok)
	}
}

// gauge is the value of the first series of name in a scrape.
func gauge(body, name string) (float64, bool) {
	for _, l := range strings.Split(body, "\n") {
		if !strings.HasPrefix(l, name+"{") && !strings.HasPrefix(l, name+" ") {
			continue
		}
		f := strings.Fields(l)
		v, err := strconv.ParseFloat(f[len(f)-1], 64)
		return v, err == nil
	}
	return 0, false
}

// Run checks once at start, then at every tick, and returns when ctx ends.
func TestRunChecksAtStartAndAtEveryTick(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	c := &clock{now: local(8, 1, 0)}
	s := f.schedule(t, c)
	ticks := make(chan time.Time)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); s.Run(ctx, ticks) }()
	ticks <- time.Time{} // received only after the start check finished
	if got := f.nightlies(t); len(got) != 1 {
		t.Errorf("after the start check the folder holds %q, want one catch-up copy", got)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}
