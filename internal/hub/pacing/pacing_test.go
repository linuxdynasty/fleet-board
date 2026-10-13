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

var weekReset = time.Date(2026, time.September, 30, 22, 0, 0, 0, time.UTC)

func pct(v uint32) *typev1.Percent {
	return &typev1.Percent{Kind: &typev1.Percent_Value{Value: v}}
}

func unknownPercent() *typev1.Percent {
	return &typev1.Percent{Kind: &typev1.Percent_Unknown{
		Unknown: &typev1.PercentUnknown{Reason: typev1.UnknownReason_UNKNOWN_REASON_NOT_SHOWN},
	}}
}

func weeklyBucket(percent uint32, reset time.Time, window time.Duration) *typev1.LimitBucket {
	return &typev1.LimitBucket{
		Key:         "weekly_all",
		PercentUsed: pct(percent),
		ResetsTime:  &typev1.ResetTime{ResolvedTime: timestamppb.New(reset)},
		Window:      durationpb.New(window),
		Kind:        typev1.WindowKind_WINDOW_KIND_WEEKLY,
	}
}

func weeklyNoWindow(percent uint32, reset time.Time) *typev1.LimitBucket {
	b := weeklyBucket(percent, reset, 0)
	b.Window = nil
	return b
}

func shortBucket(key string, percent uint32, reset time.Time) *typev1.LimitBucket {
	return &typev1.LimitBucket{
		Key:         key,
		PercentUsed: pct(percent),
		ResetsTime:  &typev1.ResetTime{ResolvedTime: timestamppb.New(reset)},
		Kind:        typev1.WindowKind_WINDOW_KIND_SHORT,
	}
}

func perModelBucket(key, model string, percent uint32, reset time.Time, window time.Duration) *typev1.LimitBucket {
	return &typev1.LimitBucket{
		Key:         key,
		Model:       model,
		PercentUsed: pct(percent),
		ResetsTime:  &typev1.ResetTime{ResolvedTime: timestamppb.New(reset)},
		Window:      durationpb.New(window),
		Kind:        typev1.WindowKind_WINDOW_KIND_PER_MODEL,
	}
}

func baseInput() pacing.Input {
	return pacing.Input{
		ReservePercent: 10,
		Budget:         typev1.BudgetKind_BUDGET_KIND_SUBSCRIPTION_PERCENT,
		Mode:           hubv1.AccountMode_ACCOUNT_MODE_FLEET,
		Overage:        typev1.OverageBilling_OVERAGE_BILLING_OFF,
		StaleAfter:     pacing.DefaultStaleAfter,
	}
}

func usageAt(read time.Time, buckets ...*typev1.LimitBucket) *hubv1.AccountUsage {
	return &hubv1.AccountUsage{
		ReadTime: timestamppb.New(read),
		Parse:    typev1.ParseOutcome_PARSE_OUTCOME_OK,
		Buckets:  buckets,
	}
}

type want struct {
	state    hubv1.PacingState
	allowed  uint32
	ahead    int32
	deciding string
	window   pacing.WindowSource
	stale    bool
	proj     *int32
}

func proj(v int32) *int32 { return &v }

func check(t *testing.T, got pacing.Result, w want) {
	t.Helper()
	p := got.Pacing
	if p.GetState() != w.state {
		t.Errorf("state = %v, want %v", p.GetState(), w.state)
	}
	if p.GetAllowedActive() != w.allowed {
		t.Errorf("allowed_active = %d, want %d", p.GetAllowedActive(), w.allowed)
	}
	if p.GetAheadPoints() != w.ahead {
		t.Errorf("ahead_points = %d, want %d", p.GetAheadPoints(), w.ahead)
	}
	if p.GetDecidingBucket() != w.deciding {
		t.Errorf("deciding_bucket = %q, want %q", p.GetDecidingBucket(), w.deciding)
	}
	if got.Window != w.window {
		t.Errorf("window = %v, want %v", got.Window, w.window)
	}
	if p.GetReadingStale() != w.stale {
		t.Errorf("reading_stale = %v, want %v", p.GetReadingStale(), w.stale)
	}
	hasProj := p.ProjectedUnusedPercent != nil
	if w.proj == nil {
		if hasProj {
			t.Errorf("projected_unused_percent = %d, want unset", p.GetProjectedUnusedPercent())
		}
	} else if !hasProj {
		t.Errorf("projected_unused_percent unset, want %d", *w.proj)
	} else if p.GetProjectedUnusedPercent() != *w.proj {
		t.Errorf("projected_unused_percent = %d, want %d", p.GetProjectedUnusedPercent(), *w.proj)
	}
}

// The 03b weekly worked examples (7-day window, reserve 10, base cap 2),
// plus the window and bucket-selection edges around them.
func TestEvaluateWeekly(t *testing.T) {
	t.Parallel()
	week := 7 * 24 * time.Hour
	cases := []struct {
		name    string
		elapsed time.Duration
		used    uint32
		in      func(*pacing.Input)
		buckets func() []*typev1.LimitBucket
		want    want
	}{
		{
			name:    "first day no projection",
			elapsed: 12 * time.Hour,
			used:    10,
			want:    want{state: hubv1.PacingState_PACING_STATE_OK, allowed: 2, ahead: 4, deciding: "weekly_all", window: pacing.WindowReading},
		},
		{
			name:    "behind adds a worker",
			elapsed: 84 * time.Hour,
			used:    30,
			want:    want{state: hubv1.PacingState_PACING_STATE_BEHIND, allowed: 3, ahead: -15, deciding: "weekly_all", window: pacing.WindowReading, proj: proj(30)},
		},
		{
			name:    "on pace",
			elapsed: 84 * time.Hour,
			used:    47,
			want:    want{state: hubv1.PacingState_PACING_STATE_OK, allowed: 2, ahead: 2, deciding: "weekly_all", window: pacing.WindowReading, proj: proj(0)},
		},
		{
			name:    "ahead trims one",
			elapsed: 84 * time.Hour,
			used:    52,
			want:    want{state: hubv1.PacingState_PACING_STATE_AHEAD, allowed: 1, ahead: 7, deciding: "weekly_all", window: pacing.WindowReading, proj: proj(0)},
		},
		{
			name:    "far ahead stops dispatch",
			elapsed: 84 * time.Hour,
			used:    56,
			want:    want{state: hubv1.PacingState_PACING_STATE_AHEAD, allowed: 0, ahead: 11, deciding: "weekly_all", window: pacing.WindowReading, proj: proj(0)},
		},
		{
			name:    "last day ignores the line",
			elapsed: 150 * time.Hour,
			used:    60,
			want:    want{state: hubv1.PacingState_PACING_STATE_OK, allowed: 2, ahead: -20, deciding: "weekly_all", window: pacing.WindowReading, proj: proj(23)},
		},
		{
			name:    "reserve stops dispatch",
			elapsed: 84 * time.Hour,
			used:    90,
			want:    want{state: hubv1.PacingState_PACING_STATE_RESERVE, allowed: 0, deciding: "weekly_all", proj: proj(0)},
		},
		{
			name:    "exactly five ahead is ok",
			elapsed: 84 * time.Hour,
			used:    50,
			want:    want{state: hubv1.PacingState_PACING_STATE_OK, allowed: 2, ahead: 5, deciding: "weekly_all", window: pacing.WindowReading, proj: proj(0)},
		},
		{
			name:    "exactly ten ahead trims",
			elapsed: 84 * time.Hour,
			used:    55,
			want:    want{state: hubv1.PacingState_PACING_STATE_AHEAD, allowed: 1, ahead: 10, deciding: "weekly_all", window: pacing.WindowReading, proj: proj(0)},
		},
		{
			name:    "exactly five behind is ok",
			elapsed: 84 * time.Hour,
			used:    40,
			want:    want{state: hubv1.PacingState_PACING_STATE_OK, allowed: 2, ahead: -5, deciding: "weekly_all", window: pacing.WindowReading, proj: proj(10)},
		},
		{
			name:    "weekly without window assumes seven days",
			elapsed: 84 * time.Hour,
			used:    30,
			buckets: func() []*typev1.LimitBucket { return []*typev1.LimitBucket{weeklyNoWindow(30, weekReset)} },
			want:    want{state: hubv1.PacingState_PACING_STATE_BEHIND, allowed: 3, ahead: -15, deciding: "weekly_all", window: pacing.WindowSevenDayDefault, proj: proj(30)},
		},
		{
			name:    "other model bucket ignored",
			elapsed: 84 * time.Hour,
			used:    30,
			in:      func(in *pacing.Input) { in.GoalModel = "fable" },
			buckets: func() []*typev1.LimitBucket {
				return []*typev1.LimitBucket{
					weeklyBucket(30, weekReset, week),
					perModelBucket("weekly_other", "other", 99, weekReset, week),
				}
			},
			want: want{state: hubv1.PacingState_PACING_STATE_BEHIND, allowed: 3, ahead: -15, deciding: "weekly_all", window: pacing.WindowReading, proj: proj(30)},
		},
		{
			name:    "goal model bucket decides",
			elapsed: 84 * time.Hour,
			used:    30,
			in:      func(in *pacing.Input) { in.GoalModel = "fable" },
			buckets: func() []*typev1.LimitBucket {
				return []*typev1.LimitBucket{
					weeklyBucket(30, weekReset, week),
					perModelBucket("weekly_fable", "fable", 99, weekReset, week),
				}
			},
			want: want{state: hubv1.PacingState_PACING_STATE_RESERVE, allowed: 0, deciding: "weekly_fable", proj: proj(0)},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			now := weekReset.Add(-week + tc.elapsed)
			in := baseInput()
			if tc.in != nil {
				tc.in(&in)
			}
			buckets := []*typev1.LimitBucket{weeklyBucket(tc.used, weekReset, week)}
			if tc.buckets != nil {
				buckets = tc.buckets()
			}
			in.Usage = usageAt(now.Add(-time.Minute), buckets...)
			check(t, pacing.Evaluate(now, in), tc.want)
		})
	}
}
