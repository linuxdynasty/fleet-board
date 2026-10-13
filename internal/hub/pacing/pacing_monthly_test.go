package pacing_test

import (
	"testing"
	"time"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/pacing"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

var monthReset = time.Date(2026, time.November, 4, 5, 0, 0, 0, time.UTC)

func monthlyBucket(key string, percent uint32, reset time.Time, window time.Duration) *typev1.LimitBucket {
	return &typev1.LimitBucket{
		Key:         key,
		PercentUsed: pct(percent),
		ResetsTime:  &typev1.ResetTime{ResolvedTime: timestamppb.New(reset), DateOnly: true},
		Window:      durationpb.New(window),
		Kind:        typev1.WindowKind_WINDOW_KIND_MONTHLY,
	}
}

func monthlyNoWindow(key string, percent uint32, reset time.Time) *typev1.LimitBucket {
	b := monthlyBucket(key, percent, reset, 0)
	b.Window = nil
	return b
}

// The 03b monthly worked examples (reserve 10, base cap 2): the worst of the
// three Cursor buckets decides, and a monthly bucket never gets 7 days.
func TestEvaluateMonthly(t *testing.T) {
	t.Parallel()
	day := 24 * time.Hour
	cases := []struct {
		name    string
		window  time.Duration
		elapsed time.Duration
		buckets func() []*typev1.LimitBucket
		seen    func(*pacing.Input)
		want    want
	}{
		{
			name:    "auto decides",
			window:  30 * day,
			elapsed: 12 * day,
			buckets: func() []*typev1.LimitBucket {
				return []*typev1.LimitBucket{
					monthlyBucket("monthly_included", 40, monthReset, 30*day),
					monthlyBucket("monthly_auto", 45, monthReset, 30*day),
					monthlyBucket("monthly_api", 0, monthReset, 30*day),
				}
			},
			want: want{state: hubv1.PacingState_PACING_STATE_AHEAD, allowed: 1, ahead: 9, deciding: "monthly_auto", window: pacing.WindowReading, proj: proj(0)},
		},
		{
			name:    "longer month same day and percent",
			window:  31 * day,
			elapsed: 12 * day,
			buckets: func() []*typev1.LimitBucket {
				return []*typev1.LimitBucket{
					monthlyBucket("monthly_included", 45, monthReset, 31*day),
					monthlyBucket("monthly_auto", 40, monthReset, 31*day),
					monthlyBucket("monthly_api", 0, monthReset, 31*day),
				}
			},
			want: want{state: hubv1.PacingState_PACING_STATE_AHEAD, allowed: 0, ahead: 10, deciding: "monthly_included", window: pacing.WindowReading, proj: proj(0)},
		},
		{
			name:    "february",
			window:  28 * day,
			elapsed: 12 * day,
			buckets: func() []*typev1.LimitBucket {
				return []*typev1.LimitBucket{
					monthlyBucket("monthly_included", 45, monthReset, 28*day),
					monthlyBucket("monthly_auto", 10, monthReset, 28*day),
					monthlyBucket("monthly_api", 0, monthReset, 28*day),
				}
			},
			want: want{state: hubv1.PacingState_PACING_STATE_AHEAD, allowed: 1, ahead: 6, deciding: "monthly_included", window: pacing.WindowReading, proj: proj(0)},
		},
		{
			name:    "first day no projection",
			window:  30 * day,
			elapsed: 12 * time.Hour,
			buckets: func() []*typev1.LimitBucket {
				return []*typev1.LimitBucket{monthlyBucket("monthly_included", 3, monthReset, 30*day)}
			},
			want: want{state: hubv1.PacingState_PACING_STATE_OK, allowed: 2, ahead: 2, deciding: "monthly_included", window: pacing.WindowReading},
		},
		{
			name:    "behind adds a worker",
			window:  30 * day,
			elapsed: 15 * day,
			buckets: func() []*typev1.LimitBucket {
				return []*typev1.LimitBucket{monthlyBucket("monthly_included", 30, monthReset, 30*day)}
			},
			want: want{state: hubv1.PacingState_PACING_STATE_BEHIND, allowed: 3, ahead: -15, deciding: "monthly_included", window: pacing.WindowReading, proj: proj(30)},
		},
		{
			// 12 h out the exact decimal ahead is -18.5, a rounding
			// knife-edge that lands -18 on macOS and -19 on Linux. 18 h
			// out the decimal is -17.75, which rounds to -18 on every
			// platform. The verdict, the allowance and the projection
			// carry the row's meaning.
			name:    "last day ignores the line",
			window:  30 * day,
			elapsed: 29*day + 6*time.Hour,
			buckets: func() []*typev1.LimitBucket {
				return []*typev1.LimitBucket{monthlyBucket("monthly_included", 70, monthReset, 30*day)}
			},
			want: want{state: hubv1.PacingState_PACING_STATE_OK, allowed: 2, ahead: -18, deciding: "monthly_included", window: pacing.WindowReading, proj: proj(18)},
		},
		{
			name:    "unknown window is unknown never seven days",
			window:  0,
			elapsed: 12 * day,
			buckets: func() []*typev1.LimitBucket {
				return []*typev1.LimitBucket{monthlyNoWindow("monthly_included", 20, monthReset)}
			},
			want: want{state: hubv1.PacingState_PACING_STATE_UNKNOWN, window: pacing.WindowNone},
		},
		{
			name:    "reserve still applies without a window",
			window:  0,
			elapsed: 12 * day,
			buckets: func() []*typev1.LimitBucket {
				return []*typev1.LimitBucket{
					monthlyNoWindow("monthly_included", 90, monthReset),
					monthlyNoWindow("monthly_auto", 20, monthReset),
				}
			},
			want: want{state: hubv1.PacingState_PACING_STATE_RESERVE, allowed: 0, deciding: "monthly_included"},
		},
		{
			name:    "seen window draws the line",
			window:  0,
			elapsed: 15 * day,
			buckets: func() []*typev1.LimitBucket {
				return []*typev1.LimitBucket{monthlyNoWindow("monthly_included", 30, monthReset)}
			},
			seen: func(in *pacing.Input) { in.SeenWindow, in.HaveSeenWindow = 30*day, true },
			want: want{state: hubv1.PacingState_PACING_STATE_BEHIND, allowed: 3, ahead: -15, deciding: "monthly_included", window: pacing.WindowSeenResets, proj: proj(30)},
		},
		{
			name: "seen window wins over the reading past an hour",
			// now is 12 days into the SEEN 30-day window; the reading's
			// 31 days only show the reset text.
			window:  30 * day,
			elapsed: 12 * day,
			buckets: func() []*typev1.LimitBucket {
				return []*typev1.LimitBucket{monthlyBucket("monthly_included", 40, monthReset, 31*day)}
			},
			seen: func(in *pacing.Input) { in.SeenWindow, in.HaveSeenWindow = 30*day, true },
			// On 30 days: target 36, ahead +4, OK. On 31 days it would be
			// +5.2, AHEAD with one fewer worker.
			want: want{state: hubv1.PacingState_PACING_STATE_OK, allowed: 2, ahead: 4, deciding: "monthly_included", window: pacing.WindowSeenOverridesReading, proj: proj(0)},
		},
		{
			name:    "seen window agreeing with the reading",
			window:  30 * day,
			elapsed: 15 * day,
			buckets: func() []*typev1.LimitBucket {
				return []*typev1.LimitBucket{monthlyBucket("monthly_included", 30, monthReset, 30*day)}
			},
			seen: func(in *pacing.Input) { in.SeenWindow, in.HaveSeenWindow = 30*day, true },
			want: want{state: hubv1.PacingState_PACING_STATE_BEHIND, allowed: 3, ahead: -15, deciding: "monthly_included", window: pacing.WindowSeenResets, proj: proj(30)},
		},
		{
			name:    "missing row is unknown",
			window:  30 * day,
			elapsed: 12 * day,
			buckets: func() []*typev1.LimitBucket {
				b := monthlyBucket("monthly_auto", 0, monthReset, 30*day)
				b.PercentUsed = unknownPercent()
				return []*typev1.LimitBucket{
					monthlyBucket("monthly_included", 40, monthReset, 30*day),
					b,
				}
			},
			want: want{state: hubv1.PacingState_PACING_STATE_UNKNOWN, window: pacing.WindowNone},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var now time.Time
			if tc.window > 0 {
				now = monthReset.Add(-tc.window + tc.elapsed)
			} else if tc.seen != nil {
				now = monthReset.Add(-30*day + tc.elapsed)
			} else {
				now = monthReset.Add(-30*day + tc.elapsed)
			}
			in := baseInput()
			if tc.seen != nil {
				tc.seen(&in)
			}
			in.Usage = usageAt(now.Add(-time.Minute), tc.buckets()...)
			check(t, pacing.Evaluate(now, in), tc.want)
		})
	}
}
