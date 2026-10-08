package backup_test

import (
	"testing"
	"time"
)

// A nightly copy stamped more than 10 minutes ahead of the clock (the clock
// jumped forward, then back) is not the newest copy: the 03:00 copy is still
// made, and backup.stamp_ahead warns once, not at every check.
func TestACopyStampedAheadOfTheClockDoesNotStopTheNightlyJob(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.touch(t, nightlyName(local(7, 3, 0)))
	f.touch(t, nightlyName(local(9, 3, 0))) // a day ahead
	c := &clock{}
	s := f.schedule(t, c)
	check(s, c, local(8, 2, 0))
	check(s, c, local(8, 3, 0))
	check(s, c, local(8, 3, 1))
	got := f.nightlies(t)
	want := []string{nightlyName(local(7, 3, 0)), nightlyName(local(8, 3, 0)), nightlyName(local(9, 3, 0))}
	if !equal(got, want) {
		t.Errorf("nightly copies %q, want %q: the 03:00 copy was not made", got, want)
	}
	ev := f.logs.events(t, "backup.stamp_ahead")
	if len(ev) != 1 || ev[0]["level"] != "warn" || ev[0]["files"] != float64(1) || ev[0]["ahead_seconds"] != float64(25*60*60) {
		t.Errorf("backup.stamp_ahead lines = %v, want one warning with files 1 and ahead_seconds 90000", ev)
	}
}

// The gauge reads the newest copy not stamped more than 10 minutes ahead;
// one up to 10 minutes ahead counts, as 0.
func TestBackupAgeSkipsACopyStampedAheadOfTheClock(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		ahead time.Duration
		want  float64
	}{
		{name: "an hour ahead is skipped", ahead: time.Hour, want: 7200},
		{name: "ten minutes ahead counts", ahead: 10 * time.Minute, want: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			c := &clock{now: local(8, 12, 0)}
			f.schedule(t, c)
			f.touch(t, manualName(local(8, 10, 0)))
			f.touch(t, nightlyName(local(8, 12, 0).Add(tc.ahead)))
			if v, ok := gauge(f.scrape(t), "fleet_hub_backup_age_seconds"); !ok || v != tc.want {
				t.Errorf("fleet_hub_backup_age_seconds = %v (present %v), want %v", v, ok, tc.want)
			}
		})
	}
}
