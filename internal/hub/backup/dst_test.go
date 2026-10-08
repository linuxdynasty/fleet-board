package backup_test

import (
	"testing"
	"time"
	_ "time/tzdata" // the zones below on any machine
)

func utc(month time.Month, day, hour, minute int) time.Time {
	return time.Date(2026, month, day, hour, minute, 0, 0, time.UTC)
}

// Across a daylight-saving change the nightly copy is made exactly once, at
// the first check at or after 03:00 local: when 03:00 does not exist, at the
// first check after the clocks jump past it; when the 03:00 hour happens
// twice, at the first. Checks run every minute through the night, and the
// previous copy is from 03:00 the day before (under 24 h old, so no
// catch-up). The instants are worked out by hand from each zone's rules.
func TestTheNightlyCopyIsMadeOnceAcrossADaylightSavingChange(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, zone string
		prior      time.Time // the previous nightly copy
		from, to   time.Time // the checks, every minute
		want       time.Time // the one new copy
	}{
		{
			// EET to EEST at 03:00 local (01:00 UTC): 03:00 to 03:59 does not
			// exist, and 04:00 EEST is 01:00 UTC.
			name: "Helsinki spring forward", zone: "Europe/Helsinki",
			prior: utc(time.March, 28, 1, 0), from: utc(time.March, 29, 0, 30), to: utc(time.March, 29, 1, 30),
			want: utc(time.March, 29, 1, 0),
		},
		{
			// EEST to EET at 04:00 local (01:00 UTC): 03:00 EEST (00:00 UTC)
			// and 03:00 EET (01:00 UTC) both happen.
			name: "Helsinki fall back", zone: "Europe/Helsinki",
			prior: utc(time.October, 24, 0, 0), from: utc(time.October, 24, 23, 30), to: utc(time.October, 25, 2, 30),
			want: utc(time.October, 25, 0, 0),
		},
		{
			// EST to EDT at 02:00 local (07:00 UTC): 03:00 EDT is 07:00 UTC.
			name: "New York spring forward", zone: "America/New_York",
			prior: utc(time.March, 7, 8, 0), from: utc(time.March, 8, 6, 30), to: utc(time.March, 8, 8, 30),
			want: utc(time.March, 8, 7, 0),
		},
		{
			// EDT to EST at 02:00 local (06:00 UTC): the 01:00 hour happens
			// twice; 03:00 EST is 08:00 UTC.
			name: "New York fall back", zone: "America/New_York",
			prior: utc(time.October, 31, 7, 0), from: utc(time.November, 1, 4, 30), to: utc(time.November, 1, 9, 30),
			want: utc(time.November, 1, 8, 0),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			loc, err := time.LoadLocation(tc.zone)
			if err != nil {
				t.Fatal(err)
			}
			f := newFixture(t)
			f.touch(t, nightlyName(tc.prior))
			c := &clock{}
			s := f.schedule(t, c)
			for at := tc.from; !at.After(tc.to); at = at.Add(time.Minute) {
				check(s, c, at.In(loc))
			}
			want := []string{nightlyName(tc.prior), nightlyName(tc.want)}
			if got := f.nightlies(t); !equal(got, want) {
				t.Errorf("nightly copies %q, want %q", got, want)
			}
		})
	}
}
