package conn

import (
	"testing"
	"time"
)

// 02i "Clocks": hub time is HelloAck.hub_time plus the time elapsed since
// the HelloAck arrived, read from the monotonic clock, so setting the
// agent's wall clock does not move it. Loop.since stands in for that
// monotonic clock here, so the wall clock can be "stepped": the HelloAck
// arrived just now by the wall clock, but an hour ago by the monotonic one
// (the wall clock was set back an hour). Hub time is hub_time plus the
// hour; anything that measures elapsed time from wall readings gets 0.
func TestHubTimeFollowsElapsedTimeFromTheHelloAck(t *testing.T) {
	t.Parallel()
	hubAt := time.Date(2031, 3, 4, 5, 6, 7, 0, time.UTC)
	cases := []struct {
		name    string
		elapsed time.Duration // what the monotonic clock says since arrival
		want    time.Time
	}{
		{"an hour by the monotonic clock, none by the wall clock", time.Hour, hubAt.Add(time.Hour)},
		{"no time at all", 0, hubAt},
	}
	for _, tc := range cases {
		l := &Loop{since: func(time.Time) time.Duration { return tc.elapsed }}
		s := &session{l: l, hubAt: hubAt, hubRecv: time.Now()}
		if got := s.hubNow(); got.Sub(tc.want).Abs() > time.Second {
			t.Errorf("%s: hubNow = %v, want about %v", tc.name, got, tc.want)
		}
	}
}
