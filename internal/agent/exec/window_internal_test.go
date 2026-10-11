package exec

import (
	"testing"
	"time"
)

// One command_id from before the window, checked at four connects 5 s
// apart (its result write kept failing), is counted once, not once per
// connect: the window holds one stamp and never refuses it at limit 3.
func TestAnOldEntryIsCountedOncePerID(t *testing.T) {
	t.Parallel()
	w := &rateWindow{times: map[string][]stamp{}}
	t0 := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)
	old := t0.Add(-2 * time.Minute)
	for i := range 4 {
		now := t0.Add(time.Duration(i*5) * time.Second)
		if wait, over := w.overCounting("K", "old-1", now, old, 3); over {
			t.Errorf("connect %d: the entry refused itself (wait %v)", i+1, wait)
		}
		if n := len(w.recent("K", now)); n != 1 {
			t.Errorf("connect %d: the window holds %d stamps, want 1", i+1, n)
		}
	}
}

// A clock that went back an hour: two stamps from 10:00 seen at 09:00.
// The window frees within a minute and retry_after is never negative.
func TestAClockGoneBackFreesTheWindowWithinAMinute(t *testing.T) {
	t.Parallel()
	w := &rateWindow{times: map[string][]stamp{}}
	ten := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)
	w.add("K", "a", ten, ten)
	w.add("K", "b", ten, ten)
	nine := ten.Add(-time.Hour)
	if wait, over := w.overCounting("K", "c", nine, nine.Add(-2*time.Minute), 2); over && (wait < 0 || wait > time.Minute) {
		t.Errorf("at 09:00: wait %v, want between 0 and a minute", wait)
	}
	later := nine.Add(61 * time.Second)
	if wait, over := w.overCounting("K", "d", later, later.Add(-2*time.Minute), 2); over {
		t.Errorf("at 09:01:01 the window is still full (wait %v), want freed", wait)
	}
}
