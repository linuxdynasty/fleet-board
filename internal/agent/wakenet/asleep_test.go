package wakenet

import (
	"errors"
	"testing"
	"time"
)

const (
	testSleepClock int32 = 7
	testAwakeClock int32 = 8
)

func TestAsleepFrom(t *testing.T) {
	t.Parallel()
	const ms = time.Millisecond
	tests := []struct {
		name    string
		reads   []time.Duration // in call order: awake, sleep, awake, awake, ...
		failAt  int             // 1-based read that fails; 0 for none
		want    time.Duration
		wantErr error
	}{
		{
			name:  "sleep clock minus the midpoint of the awake reads",
			reads: []time.Duration{100 * ms, 160 * ms, 102 * ms},
			want:  59 * ms,
		},
		{
			name:  "equal clocks read later give a tiny negative, not an error",
			reads: []time.Duration{100 * ms, 100 * ms, 100*ms + 418},
			want:  -209,
		},
		{
			name: "a wide window is read again",
			reads: []time.Duration{
				100 * ms, 3100 * ms, 200 * ms,
				300 * ms, 3300 * ms, 301 * ms,
			},
			want: 3*time.Second - ms/2,
		},
		{
			name: "three wide windows are an error",
			reads: []time.Duration{
				0, 0, 50 * ms, 0, 0, 50 * ms, 0, 0, 50 * ms,
			},
			wantErr: errClocksApart,
		},
		{
			name:    "a failed read is an error",
			reads:   []time.Duration{0, 0, 0},
			failAt:  2,
			wantErr: errClock,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			n := 0
			read := func(id int32) (time.Duration, error) {
				n++
				if n == tc.failAt {
					return 0, errClock
				}
				want := testAwakeClock
				if n%3 == 2 {
					want = testSleepClock
				}
				if id != want {
					t.Fatalf("read %d asked clock %d, want %d", n, id, want)
				}
				return tc.reads[n-1], nil
			}
			got, err := asleepFrom(read, testSleepClock, testAwakeClock)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if err == nil && got != tc.want {
				t.Errorf("asleep = %v, want %v", got, tc.want)
			}
		})
	}
}
