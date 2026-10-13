package harness

import (
	"testing"
	"time"
)

func TestNextMonthDayTime(t *testing.T) {
	t.Parallel()
	read := time.Date(2026, time.October, 10, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name                       string
		month                      time.Month
		day, hour, minute          int
		readTime                   time.Time
		wantYear                   int
		wantMonth                  time.Month
		wantDay, wantHour, wantMin int
		wantNil                    bool
	}{
		{"later this year", time.October, 14, 22, 0, read, 2026, time.October, 14, 22, 0, false},
		{"already passed rolls a year", time.October, 9, 22, 0, read, 2027, time.October, 9, 22, 0, false},
		{"same day later time", time.October, 10, 18, 30, read, 2026, time.October, 10, 18, 30, false},
		{"exact read moment rolls a year", time.October, 10, 12, 0, read, 2027, time.October, 10, 12, 0, false},
		{"feb 30 never resolves", time.February, 30, 8, 0, read, 0, 0, 0, 0, 0, true},
		{
			"feb 29 finds the next leap year", time.February, 29, 8, 0,
			time.Date(2026, time.March, 1, 0, 0, 0, 0, time.UTC), 2028, time.February, 29, 8, 0, false,
		},
		{"day zero never resolves", time.October, 0, 8, 0, read, 0, 0, 0, 0, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := NextMonthDayTime(tc.month, tc.day, tc.hour, tc.minute, time.UTC, tc.readTime, "Resets shown")
			if tc.wantNil {
				if got != nil {
					t.Fatalf("got %v, want nil", got.GetResolvedTime().AsTime())
				}
				return
			}
			if got == nil {
				t.Fatal("got nil, want a resolved reset")
			}
			rt := got.GetResolvedTime().AsTime().UTC()
			y, m, d := rt.Date()
			h, min, _ := rt.Clock()
			if y != tc.wantYear || m != tc.wantMonth || d != tc.wantDay || h != tc.wantHour || min != tc.wantMin {
				t.Errorf("reset = %v, want %d-%v-%d %02d:%02d UTC", rt, tc.wantYear, tc.wantMonth, tc.wantDay, tc.wantHour, tc.wantMin)
			}
			if got.GetOriginalText() != "Resets shown" {
				t.Errorf("original text = %q, want %q", got.GetOriginalText(), "Resets shown")
			}
			if got.GetResolvedWithTimezone() != "UTC" {
				t.Errorf("timezone = %q, want UTC", got.GetResolvedWithTimezone())
			}
			if got.GetDateOnly() {
				t.Errorf("date_only set on a reset that showed a time")
			}
		})
	}
}

func TestNextTimeOfDay(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name              string
		hour, minute      int
		readTime          time.Time
		wantDay, wantHour int
	}{
		{"later today", 18, 30, time.Date(2026, time.October, 10, 12, 0, 0, 0, time.UTC), 10, 18},
		{"already passed is tomorrow", 6, 30, time.Date(2026, time.October, 10, 12, 0, 0, 0, time.UTC), 11, 6},
		{"tomorrow crosses the month", 6, 30, time.Date(2026, time.October, 31, 12, 0, 0, 0, time.UTC), 1, 6},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := NextTimeOfDay(tc.hour, tc.minute, time.UTC, tc.readTime, "Resets shown")
			if got == nil {
				t.Fatal("got nil, want a resolved reset")
			}
			rt := got.GetResolvedTime().AsTime().UTC()
			if rt.Day() != tc.wantDay || rt.Hour() != tc.wantHour {
				t.Errorf("reset = %v, want day %d at %02d:00 UTC", rt, tc.wantDay, tc.wantHour)
			}
		})
	}
}

func TestClock12(t *testing.T) {
	t.Parallel()
	cases := []struct {
		hour, minute int
		pm           bool
		wantHour     int
		ok           bool
	}{
		{12, 0, false, 0, true},
		{12, 30, true, 12, true},
		{6, 30, true, 18, true},
		{10, 0, true, 22, true},
		{5, 17, false, 5, true},
		{0, 0, false, 0, false},
		{13, 0, true, 0, false},
		{6, 60, true, 0, false},
		{6, -1, true, 0, false},
	}
	for _, tc := range cases {
		h, m, ok := Clock12(tc.hour, tc.minute, tc.pm)
		if ok != tc.ok || (ok && (h != tc.wantHour || m != tc.minute)) {
			t.Errorf("Clock12(%d, %d, %v) = %d, %d, %v; want hour %d, ok %v",
				tc.hour, tc.minute, tc.pm, h, m, ok, tc.wantHour, tc.ok)
		}
	}
}

func TestMonthFromShort(t *testing.T) {
	t.Parallel()
	if m, ok := MonthFromShort("Oct"); !ok || m != time.October {
		t.Errorf("MonthFromShort(Oct) = %v, %v; want October, true", m, ok)
	}
	if _, ok := MonthFromShort("Foo"); ok {
		t.Errorf("MonthFromShort(Foo) ok, want not ok")
	}
}
