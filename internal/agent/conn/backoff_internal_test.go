package conn

import (
	"testing"
	"time"
)

// defaults.md "Reconnect backoff": 1 to 60 s with full jitter on every
// wait, the first one included: after n failures in a row the wait is
// uniform from 1 s up to 2^n s, capped at 60 s.
func TestTheBackoffGrowsToItsCapWithFullJitter(t *testing.T) {
	t.Parallel()
	b := Backoff{}.withDefaults()
	cases := []struct {
		failures int
		lo, hi   time.Duration
	}{
		{1, time.Second, 2 * time.Second},
		{2, time.Second, 4 * time.Second},
		{3, time.Second, 8 * time.Second},
		{5, time.Second, 32 * time.Second},
		{6, time.Second, 60 * time.Second}, // 64 s is past the cap
		{30, time.Second, 60 * time.Second},
		{31, time.Second, 60 * time.Second}, // a shift this far would overflow
		{63, time.Second, 60 * time.Second},
		{1000, time.Second, 60 * time.Second},
	}
	for _, tc := range cases {
		seen := map[time.Duration]bool{}
		var top time.Duration
		for range 500 {
			d := b.wait(tc.failures)
			if d < tc.lo || d > tc.hi {
				t.Fatalf("wait(%d) = %v, want within [%v, %v]", tc.failures, d, tc.lo, tc.hi)
			}
			seen[d] = true
			top = max(top, d)
		}
		// Full jitter: the waits spread over the range, not one value.
		if len(seen) < 2 || top < tc.lo+(tc.hi-tc.lo)/2 {
			t.Errorf("wait(%d) over 500 tries: %d distinct values, highest %v; want spread up to %v",
				tc.failures, len(seen), top, tc.hi)
		}
	}
}

// Zero fields take the defaults.md values.
func TestAZeroBackoffTakesTheDefaults(t *testing.T) {
	t.Parallel()
	want := Backoff{
		First: time.Second, Max: 60 * time.Second, MinGap: 5 * time.Second,
		EventDelayMin: 20 * time.Millisecond, EventDelayMax: 120 * time.Millisecond,
	}
	if got := (Backoff{}).withDefaults(); got != want {
		t.Errorf("Backoff{} = %+v, want %+v", got, want)
	}
	b := want
	for range 200 {
		if d := b.eventDelay(); d < b.EventDelayMin || d > b.EventDelayMax {
			t.Fatalf("eventDelay = %v, want within [%v, %v]", d, b.EventDelayMin, b.EventDelayMax)
		}
	}
}
