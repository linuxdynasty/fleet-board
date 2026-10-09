package wakenet

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
)

// fakeAsleep is the time-asleep-since-boot source a test sets before each check.
type fakeAsleep struct {
	mu  sync.Mutex
	d   time.Duration
	err error
}

func (f *fakeAsleep) set(d time.Duration, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.d, f.err = d, err
}

func (f *fakeAsleep) read() (time.Duration, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.d, f.err
}

// wakeStep is one 5 s check: how far each clock moved since the last one and
// what the time-asleep reading says.
type wakeStep struct {
	wall, mono time.Duration
	asleep     time.Duration
	err        error
}

// normal is a check 5 s after the last with nothing slept.
func normal(asleep time.Duration) wakeStep {
	return wakeStep{wall: 5 * time.Second, mono: 5 * time.Second, asleep: asleep}
}

var errClock = errors.New("clock_gettime: invalid argument")

// TestWakeChecks is the wake half of capability A6 (ADR 0029, plan row 4).
func TestWakeChecks(t *testing.T) {
	t.Parallel()
	const s = time.Second
	tests := []struct {
		name       string
		baseAsleep time.Duration
		baseErr    error
		steps      []wakeStep
		want       []WakeInfo
		logs       map[string]int
	}{
		{
			name:  "wall clock 2 s past monotonic is exactly one wake",
			steps: []wakeStep{{wall: 7 * s, mono: 5 * s}, normal(0), normal(0)},
			want:  []WakeInfo{{WallAhead: true, WallGap: 2 * s}},
			logs:  map[string]int{"wake.detected": 1},
		},
		{
			name:  "wall clock 1.9 s past monotonic is none",
			steps: []wakeStep{{wall: 5*s + 1900*time.Millisecond, mono: 5 * s}, normal(0)},
			logs:  map[string]int{"wake.detected": 0},
		},
		{
			name:  "time asleep up 2 s is a wake",
			steps: []wakeStep{normal(2 * s), normal(2 * s)},
			want:  []WakeInfo{{AsleepGrew: true, AsleepDelta: 2 * s}},
		},
		{
			name:  "time asleep up 1.9 s is none",
			steps: []wakeStep{normal(1900 * time.Millisecond), normal(1900 * time.Millisecond)},
		},
		{
			name:  "both signals in one check are one wake",
			steps: []wakeStep{{wall: 65 * s, mono: 5 * s, asleep: 60 * s}, normal(60 * s)},
			want:  []WakeInfo{{AsleepGrew: true, AsleepDelta: 60 * s, WallAhead: true, WallGap: 60 * s}},
			logs:  map[string]int{"wake.detected": 1},
		},
		{
			name:  "wall clock stepped back is logged, not a wake",
			steps: []wakeStep{{wall: -5 * s, mono: 5 * s}, normal(0)},
			logs:  map[string]int{"wake.wall_clock_back": 1, "wake.detected": 0},
		},
		{
			name:  "small wall clock slew back is not logged",
			steps: []wakeStep{{wall: 4 * s, mono: 5 * s}},
			logs:  map[string]int{"wake.wall_clock_back": 0},
		},
		{
			name:  "tiny negative time asleep is zero, not an error",
			steps: []wakeStep{normal(-209 * time.Nanosecond), normal(0), normal(-209 * time.Nanosecond)},
			logs:  map[string]int{"wake.clock_read_failed": 0, "wake.detected": 0},
		},
		{
			name:       "growth from a tiny negative baseline is measured from zero",
			baseAsleep: -209 * time.Nanosecond,
			steps:      []wakeStep{normal(2 * s)},
			want:       []WakeInfo{{AsleepGrew: true, AsleepDelta: 2 * s}},
		},
		{
			name:  "a failed clock read is logged and is no wake",
			steps: []wakeStep{{wall: 5 * s, mono: 5 * s, err: errClock}, normal(0)},
			logs:  map[string]int{"wake.clock_read_failed": 1, "wake.detected": 0},
		},
		{
			name:  "a large negative reading counts as a failed read",
			steps: []wakeStep{normal(-5 * s), normal(0)},
			logs:  map[string]int{"wake.clock_read_failed": 1, "wake.detected": 0},
		},
		{
			name:  "sleep across a failed read is still measured from the last good reading",
			steps: []wakeStep{{wall: 5 * s, mono: 5 * s, err: errClock}, normal(3 * s)},
			want:  []WakeInfo{{AsleepGrew: true, AsleepDelta: 3 * s}},
			logs:  map[string]int{"wake.clock_read_failed": 1},
		},
		{
			name:       "a failed baseline read makes the first good reading the baseline",
			baseAsleep: 0,
			baseErr:    errClock,
			steps:      []wakeStep{normal(40 * s), normal(40 * s)},
			logs:       map[string]int{"wake.clock_read_failed": 1, "wake.detected": 0},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			clock := newFakeClock()
			asleep := &fakeAsleep{}
			asleep.set(tc.baseAsleep, tc.baseErr)
			logs := newLogCapture()
			w := &Wake{Clock: clock, Asleep: asleep.read, Log: logs.logger()}
			out := make(chan Event, 16)
			stop := runLoop(t, func(ctx context.Context) { w.Run(ctx, out) })

			for _, st := range tc.steps {
				tm := clock.nextTimer(t)
				if tm.d != 5*time.Second {
					t.Fatalf("wake check timer = %v, want 5s", tm.d)
				}
				clock.move(st.wall, st.mono)
				asleep.set(st.asleep, st.err)
				tm.fire()
			}
			clock.nextTimer(t) // the last check finished, emission included
			stop()

			got := drain(out)
			if len(got) != len(tc.want) {
				t.Fatalf("got %d wake events %+v, want %d", len(got), got, len(tc.want))
			}
			for i, ev := range got {
				if ev.Reason != agentv1.ConnectReason_CONNECT_REASON_WAKE {
					t.Errorf("event %d reason = %v, want CONNECT_REASON_WAKE", i, ev.Reason)
				}
				if ev.Wake != tc.want[i] {
					t.Errorf("event %d = %+v, want %+v", i, ev.Wake, tc.want[i])
				}
			}
			for msg, n := range tc.logs {
				if c := logs.count(msg); c != n {
					t.Errorf("%s logged %d times, want %d", msg, c, n)
				}
			}
		})
	}
}

func TestWakeDetectedLogNamesTheSignals(t *testing.T) {
	t.Parallel()
	clock := newFakeClock()
	asleep := &fakeAsleep{}
	logs := newLogCapture()
	w := &Wake{Clock: clock, Asleep: asleep.read, Log: logs.logger()}
	out := make(chan Event, 4)
	stop := runLoop(t, func(ctx context.Context) { w.Run(ctx, out) })

	tm := clock.nextTimer(t)
	clock.move(35*time.Second, 5*time.Second)
	asleep.set(30*time.Second, nil)
	tm.fire()
	clock.nextTimer(t)
	stop()

	want := map[string]any{"signal": "both", "asleep_delta_ms": int64(30000), "wall_gap_ms": int64(30000)}
	for key, w := range want {
		v, ok := logs.attr("wake.detected", key)
		if !ok {
			t.Fatalf("wake.detected has no %s", key)
		}
		if v.Any() != w {
			t.Errorf("wake.detected %s = %v, want %v", key, v.Any(), w)
		}
	}
}
