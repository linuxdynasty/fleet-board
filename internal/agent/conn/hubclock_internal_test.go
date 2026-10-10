package conn

import (
	"testing"
	"time"
)

// 02i "Clocks": hub time is HelloAck.hub_time plus the time elapsed since
// the HelloAck arrived, read from the monotonic clock, so setting the
// agent's wall clock does not move it. The HelloAck here came an hour ago
// with a hub time far from the agent's wall clock: hub time now is that
// hub time plus the hour, whatever the wall clock says.
func TestHubTimeFollowsElapsedTimeFromTheHelloAck(t *testing.T) {
	t.Parallel()
	hubAt := time.Date(2031, 3, 4, 5, 6, 7, 0, time.UTC)
	cases := []struct {
		name string
		ago  time.Duration
		want time.Time
	}{
		{"hub time an hour ago", time.Hour, hubAt.Add(time.Hour)},
		{"hub time just now", 0, hubAt},
	}
	for _, tc := range cases {
		s := &session{hubAt: hubAt, hubRecv: time.Now().Add(-tc.ago)}
		if got := s.hubNow(); got.Sub(tc.want).Abs() > time.Second {
			t.Errorf("%s: hubNow = %v, want about %v", tc.name, got, tc.want)
		}
	}
}
