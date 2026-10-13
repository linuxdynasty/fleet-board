package pacing_test

import (
	"testing"
	"time"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/pacing"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Freshness, exclusion and failure precedence around the pace line.
func TestEvaluateStates(t *testing.T) {
	t.Parallel()
	week := 7 * 24 * time.Hour
	now := weekReset.Add(-week + 84*time.Hour)

	t.Run("no reading is unknown", func(t *testing.T) {
		t.Parallel()
		got := pacing.Evaluate(now, baseInput())
		check(t, got, want{state: hubv1.PacingState_PACING_STATE_UNKNOWN})
	})

	t.Run("parse failed is unknown at once", func(t *testing.T) {
		t.Parallel()
		in := baseInput()
		in.Usage = usageAt(now.Add(-time.Minute), weeklyBucket(30, weekReset, week))
		in.Usage.Parse = typev1.ParseOutcome_PARSE_OUTCOME_FAILED
		got := pacing.Evaluate(now, in)
		check(t, got, want{state: hubv1.PacingState_PACING_STATE_UNKNOWN})
	})

	t.Run("old reading is stale", func(t *testing.T) {
		t.Parallel()
		in := baseInput()
		in.Usage = usageAt(now.Add(-40*time.Minute), weeklyBucket(30, weekReset, week))
		got := pacing.Evaluate(now, in)
		check(t, got, want{state: hubv1.PacingState_PACING_STATE_STALE})
	})

	t.Run("last good reading paces until stale after", func(t *testing.T) {
		t.Parallel()
		in := baseInput()
		in.Usage = usageAt(now.Add(-20*time.Minute), weeklyBucket(30, weekReset, week))
		got := pacing.Evaluate(now, in)
		check(t, got, want{state: hubv1.PacingState_PACING_STATE_BEHIND, allowed: 3, ahead: -15, deciding: "weekly_all", window: pacing.WindowReading, proj: proj(30)})
	})

	t.Run("reading past a reset flags stale", func(t *testing.T) {
		t.Parallel()
		// Read 5 minutes before the window started, evaluated 10 minutes
		// in: fresh, but predating the last reset.
		read := weekReset.Add(-week - 5*time.Minute)
		at := weekReset.Add(-week + 10*time.Minute)
		in := baseInput()
		in.Usage = usageAt(read, weeklyBucket(2, weekReset, week))
		got := pacing.Evaluate(at, in)
		p := got.Pacing
		if !p.GetReadingStale() {
			t.Fatalf("reading_stale = false, want true")
		}
		if p.GetState() != hubv1.PacingState_PACING_STATE_OK {
			t.Fatalf("state = %v, want OK", p.GetState())
		}
		if p.ProjectedUnusedPercent != nil {
			t.Fatalf("projected_unused_percent set 10 minutes into the window, want unset")
		}
	})

	t.Run("dollar cap is excluded", func(t *testing.T) {
		t.Parallel()
		in := baseInput()
		in.Budget = typev1.BudgetKind_BUDGET_KIND_DOLLAR_CAP
		in.Usage = usageAt(now.Add(-time.Minute), weeklyBucket(30, weekReset, week))
		got := pacing.Evaluate(now, in)
		check(t, got, want{state: hubv1.PacingState_PACING_STATE_EXCLUDED})
	})

	t.Run("interactive only is excluded", func(t *testing.T) {
		t.Parallel()
		in := baseInput()
		in.Mode = hubv1.AccountMode_ACCOUNT_MODE_INTERACTIVE_ONLY
		in.Usage = usageAt(now.Add(-time.Minute), weeklyBucket(30, weekReset, week))
		got := pacing.Evaluate(now, in)
		check(t, got, want{state: hubv1.PacingState_PACING_STATE_EXCLUDED})
	})

	t.Run("overage on or unknown is excluded", func(t *testing.T) {
		t.Parallel()
		for _, over := range []typev1.OverageBilling{
			typev1.OverageBilling_OVERAGE_BILLING_ON,
			typev1.OverageBilling_OVERAGE_BILLING_UNKNOWN,
		} {
			in := baseInput()
			in.Overage = over
			in.Usage = usageAt(now.Add(-time.Minute), weeklyBucket(30, weekReset, week))
			got := pacing.Evaluate(now, in)
			if got.Pacing.GetState() != hubv1.PacingState_PACING_STATE_EXCLUDED {
				t.Fatalf("overage %v: state = %v, want EXCLUDED", over, got.Pacing.GetState())
			}
		}
	})

	t.Run("short bucket at 95 stops dispatch", func(t *testing.T) {
		t.Parallel()
		in := baseInput()
		in.Usage = usageAt(now.Add(-time.Minute),
			weeklyBucket(30, weekReset, week),
			shortBucket("session", 95, weekReset),
		)
		got := pacing.Evaluate(now, in)
		check(t, got, want{state: hubv1.PacingState_PACING_STATE_SHORT_LIMIT, deciding: "session"})
	})

	t.Run("short bucket at 94 does not", func(t *testing.T) {
		t.Parallel()
		in := baseInput()
		in.Usage = usageAt(now.Add(-time.Minute),
			weeklyBucket(30, weekReset, week),
			shortBucket("session", 94, weekReset),
		)
		got := pacing.Evaluate(now, in)
		check(t, got, want{state: hubv1.PacingState_PACING_STATE_BEHIND, allowed: 3, ahead: -15, deciding: "weekly_all", window: pacing.WindowReading, proj: proj(30)})
	})

	t.Run("reserve beats the speed limit", func(t *testing.T) {
		t.Parallel()
		in := baseInput()
		in.Usage = usageAt(now.Add(-time.Minute),
			weeklyBucket(92, weekReset, week),
			shortBucket("session", 96, weekReset),
		)
		got := pacing.Evaluate(now, in)
		if got.Pacing.GetState() != hubv1.PacingState_PACING_STATE_RESERVE {
			t.Fatalf("state = %v, want RESERVE", got.Pacing.GetState())
		}
	})

	t.Run("open usage condition stops dispatch", func(t *testing.T) {
		t.Parallel()
		in := baseInput()
		in.UsageLimitUntil = now.Add(time.Hour)
		in.Usage = usageAt(now.Add(-time.Minute), weeklyBucket(30, weekReset, week))
		got := pacing.Evaluate(now, in)
		check(t, got, want{state: hubv1.PacingState_PACING_STATE_SHORT_LIMIT, ahead: -15, deciding: "weekly_all", window: pacing.WindowReading})
	})

	t.Run("passed usage condition releases", func(t *testing.T) {
		t.Parallel()
		in := baseInput()
		in.UsageLimitUntil = now.Add(-time.Hour)
		in.Usage = usageAt(now.Add(-time.Minute), weeklyBucket(30, weekReset, week))
		got := pacing.Evaluate(now, in)
		check(t, got, want{state: hubv1.PacingState_PACING_STATE_BEHIND, allowed: 3, ahead: -15, deciding: "weekly_all", window: pacing.WindowReading, proj: proj(30)})
	})

	t.Run("unshown bucket is unknown", func(t *testing.T) {
		t.Parallel()
		b := weeklyBucket(0, weekReset, week)
		b.PercentUsed = unknownPercent()
		in := baseInput()
		in.Usage = usageAt(now.Add(-time.Minute), b)
		got := pacing.Evaluate(now, in)
		check(t, got, want{state: hubv1.PacingState_PACING_STATE_UNKNOWN})
	})

	t.Run("percent above 100 caps at full", func(t *testing.T) {
		t.Parallel()
		in := baseInput()
		in.Usage = usageAt(now.Add(-time.Minute), weeklyBucket(200, weekReset, week))
		got := pacing.Evaluate(now, in)
		if got.Pacing.GetState() != hubv1.PacingState_PACING_STATE_RESERVE {
			t.Fatalf("state = %v, want RESERVE", got.Pacing.GetState())
		}
	})

	t.Run("active goals pass through", func(t *testing.T) {
		t.Parallel()
		in := baseInput()
		in.ActiveGoals = 2
		in.Usage = usageAt(now.Add(-time.Minute), weeklyBucket(30, weekReset, week))
		got := pacing.Evaluate(now, in)
		if got.Pacing.GetActiveGoals() != 2 {
			t.Fatalf("active_goals = %d, want 2", got.Pacing.GetActiveGoals())
		}
	})

	t.Run("read time without clock is stale", func(t *testing.T) {
		t.Parallel()
		in := baseInput()
		in.Usage = usageAt(now.Add(-time.Minute), weeklyBucket(30, weekReset, week))
		in.Usage.ReadTime = nil
		got := pacing.Evaluate(now, in)
		if got.Pacing.GetState() != hubv1.PacingState_PACING_STATE_STALE {
			t.Fatalf("state = %v, want STALE", got.Pacing.GetState())
		}
	})

	t.Run("invalid read time is stale", func(t *testing.T) {
		t.Parallel()
		in := baseInput()
		in.Usage = usageAt(now.Add(-time.Minute), weeklyBucket(30, weekReset, week))
		in.Usage.ReadTime = timestamppb.New(time.Time{})
		got := pacing.Evaluate(now, in)
		if got.Pacing.GetState() != hubv1.PacingState_PACING_STATE_STALE {
			t.Fatalf("state = %v, want STALE", got.Pacing.GetState())
		}
	})
}
