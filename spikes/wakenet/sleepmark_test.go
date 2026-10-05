package main

import (
	"testing"
	"time"
)

func TestSleepMarkDetector(t *testing.T) {
	t.Parallel()
	wake1 := time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)
	wake2 := wake1.Add(3 * time.Hour)
	cases := []struct {
		name      string
		marks     []SleepMark
		wantWake  bool
		wantSlept time.Duration
		wantAt    time.Time
	}{
		{
			name:  "first reading is the baseline",
			marks: []SleepMark{{WakeTime: wake1}},
		},
		{
			name:  "macOS never slept since boot (zero waketime) and still has not",
			marks: []SleepMark{{}, {}},
		},
		{
			name:     "macOS first wake since boot",
			marks:    []SleepMark{{}, {WakeTime: wake1}},
			wantWake: true,
			wantAt:   wake1,
		},
		{
			name:     "macOS waketime moves forward",
			marks:    []SleepMark{{WakeTime: wake1}, {WakeTime: wake2}},
			wantWake: true,
			wantAt:   wake2,
		},
		{
			name:  "macOS waketime unchanged",
			marks: []SleepMark{{WakeTime: wake1}, {WakeTime: wake1}},
		},
		{
			name:      "Linux suspended total grows by 20 minutes",
			marks:     []SleepMark{{Suspended: time.Minute}, {Suspended: 21 * time.Minute}},
			wantWake:  true,
			wantSlept: 20 * time.Minute,
		},
		{
			name:  "Linux clock drift under tolerance is not a wake",
			marks: []SleepMark{{Suspended: time.Minute}, {Suspended: time.Minute + 50*time.Millisecond}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := SleepMarkDetector{Tolerance: 500 * time.Millisecond}
			var got Wake
			var woke bool
			for _, m := range tc.marks {
				got, woke = d.Observe(m)
			}
			if woke != tc.wantWake {
				t.Fatalf("woke = %v, want %v (%+v)", woke, tc.wantWake, got)
			}
			if got.Slept != tc.wantSlept || !got.At.Equal(tc.wantAt) {
				t.Fatalf("got %+v, want slept %v at %v", got, tc.wantSlept, tc.wantAt)
			}
		})
	}
}
