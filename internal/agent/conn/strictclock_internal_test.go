package conn

import (
	"errors"
	"testing"
	"time"
)

// 02i "Clocks" and ruling fork 11: the deadline clock is the last
// HelloAck's hub_time plus the larger of the monotonic and the
// sleep-counting time since; it says "not known" (ok false) rather than
// run late, so a deadline is never extended.
func TestTheStrictHubClock(t *testing.T) {
	t.Parallel()
	hubAt := time.Date(2031, 3, 4, 5, 6, 7, 0, time.UTC)
	errRead := errors.New("unreadable")
	anchored := &hubAnchor{at: hubAt, recv: time.Now(), sleep: 10 * time.Second, sleepOK: true}
	cases := []struct {
		name     string
		anchor   *hubAnchor
		mono     time.Duration // monotonic time since the HelloAck
		sleepNow time.Duration // the sleep-counting clock now
		sleepErr error
		want     time.Time
		ok       bool
	}{
		{"no HelloAck with hub_time yet", nil, time.Second, time.Hour, nil, time.Time{}, false},
		{
			"asleep for two hours, the monotonic clock counted a second", anchored, time.Second,
			10*time.Second + 2*time.Hour, nil, hubAt.Add(2 * time.Hour), true,
		},
		{
			"the monotonic clock counted more", anchored, 3 * time.Hour, 10*time.Second + time.Hour, nil,
			hubAt.Add(3 * time.Hour), true,
		},
		{
			"the sleep clock could not be read at the HelloAck", &hubAnchor{at: hubAt, recv: time.Now()},
			time.Second, time.Hour, nil,
			time.Time{},
			false,
		},
		{"the sleep clock cannot be read now", anchored, time.Second, 0, errRead, time.Time{}, false},
		{"the sleep clock went back", anchored, time.Minute, time.Second, nil, hubAt.Add(time.Minute), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			l := &Loop{
				since:      func(time.Time) time.Duration { return tc.mono },
				sleepClock: func() (time.Duration, error) { return tc.sleepNow, tc.sleepErr },
				now:        func() time.Time { return hubAt.Add(-48 * time.Hour) },
			}
			l.anchor.Store(tc.anchor)
			got, ok := l.HubClock().Now()
			if ok != tc.ok || !got.Equal(tc.want) {
				t.Errorf("Now() = %v, %v; want %v, %v", got, ok, tc.want, tc.ok)
			}
		})
	}
}
