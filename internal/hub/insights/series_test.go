package insights

import (
	"testing"
	"time"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

var (
	seriesStart = time.Date(2026, time.October, 6, 0, 0, 0, 0, time.UTC)
	seriesEnd   = time.Date(2026, time.October, 14, 0, 0, 0, 0, time.UTC)
	seriesAcct  = &typev1.AccountId{Value: "acct-1"}
)

// pointValue asserts one series point by its getter pair: in this package
// a set value always comes with a sample, so (value, sample) proves
// presence without touching the optional field.
func pointValue(t *testing.T, s *hubv1.InsightSeries, day time.Time, want float64, sample uint32) {
	t.Helper()
	for _, pt := range s.GetPoints() {
		if pt.GetStartTime().AsTime().Equal(day) {
			if pt.GetSampleSize() != sample {
				t.Errorf("sample at %v = %d, want %d", day, pt.GetSampleSize(), sample)
			}
			if sample == 0 {
				if pt.GetValue() != 0 {
					t.Errorf("value at %v = %v, want unset", day, pt.GetValue())
				}
				return
			}
			if pt.GetValue() != want {
				t.Errorf("value at %v = %v, want %v", day, pt.GetValue(), want)
			}
			return
		}
	}
	t.Errorf("no point at %v in %v", day, s.GetPoints())
}

func dayOf(monthDay int) time.Time {
	return time.Date(2026, time.October, monthDay, 0, 0, 0, 0, time.UTC)
}

func TestQuotaUsedSeries_DailyLastReading(t *testing.T) {
	t.Parallel()
	got := QuotaUsedSeries(seriesAcct, "weekly_all", weekReadings(), seriesStart, seriesEnd, hubv1.InsightGranularity_INSIGHT_GRANULARITY_DAY)
	if got.GetMetric() != hubv1.InsightMetric_INSIGHT_METRIC_QUOTA_USED_PERCENT {
		t.Errorf("metric = %v, want QUOTA_USED_PERCENT", got.GetMetric())
	}
	if got.GetUnit() != hubv1.InsightUnit_INSIGHT_UNIT_PERCENT {
		t.Errorf("unit = %v, want PERCENT", got.GetUnit())
	}
	if got.GetBucketKey() != "weekly_all" {
		t.Errorf("bucket_key = %q, want weekly_all", got.GetBucketKey())
	}
	if len(got.GetPoints()) != 8 {
		t.Fatalf("points = %d, want 8 (one per day Oct 6-13)", len(got.GetPoints()))
	}
	for i, want := range []float64{10, 20, 30, 45, 55, 65, 75, 5} {
		pointValue(t, got, dayOf(6+i), want, 1)
	}
}

func TestQuotaUsedSeries_WeeklyBins(t *testing.T) {
	t.Parallel()
	// Weeks start Monday: Oct 5 and Oct 12 over the range below.
	start := dayOf(5)
	end := time.Date(2026, time.October, 19, 0, 0, 0, 0, time.UTC)
	got := QuotaUsedSeries(seriesAcct, "weekly_all", weekReadings(), start, end, hubv1.InsightGranularity_INSIGHT_GRANULARITY_WEEK)
	if len(got.GetPoints()) != 2 {
		t.Fatalf("points = %d, want 2 (weeks of Oct 5 and Oct 12)", len(got.GetPoints()))
	}
	// Last good reading of each week: Oct 11 noon (65, six readings in
	// the week), Oct 13 noon (5, two readings: Oct 12 and Oct 13).
	pointValue(t, got, dayOf(5), 65, 6)
	pointValue(t, got, dayOf(12), 5, 2)
}

func TestQuotaUsedSeries_DefaultsToDaily(t *testing.T) {
	t.Parallel()
	got := QuotaUsedSeries(seriesAcct, "weekly_all", weekReadings(), seriesStart, seriesEnd, hubv1.InsightGranularity_INSIGHT_GRANULARITY_UNSPECIFIED)
	if len(got.GetPoints()) != 8 {
		t.Errorf("points = %d, want 8 (unspecified defaults to DAY under 90 days)", len(got.GetPoints()))
	}
}

func TestQuotaUnusedSeries_WasteAtReset(t *testing.T) {
	t.Parallel()
	wins := Windows(weekInput()).GetWindows()
	got := QuotaUnusedSeries(seriesAcct, "weekly_all", wins, seriesStart, seriesEnd, hubv1.InsightGranularity_INSIGHT_GRANULARITY_DAY)
	if got.GetMetric() != hubv1.InsightMetric_INSIGHT_METRIC_QUOTA_UNUSED_PERCENT {
		t.Errorf("metric = %v, want QUOTA_UNUSED_PERCENT", got.GetMetric())
	}
	// Only the complete window's reset (Oct 13, waste 15) plots; the
	// open window has no waste yet and the other days have no reset.
	pointValue(t, got, dayOf(13), 15, 1)
	pointValue(t, got, dayOf(6), 0, 0)
	pointValue(t, got, dayOf(12), 0, 0)
}

func TestIdleBehindSeries_SecondsPerDay(t *testing.T) {
	t.Parallel()
	got := IdleBehindSeries(seriesAcct, weekInput().Pace, seriesStart, seriesEnd, hubv1.InsightGranularity_INSIGHT_GRANULARITY_DAY)
	if got.GetMetric() != hubv1.InsightMetric_INSIGHT_METRIC_IDLE_BEHIND_SECONDS {
		t.Errorf("metric = %v, want IDLE_BEHIND_SECONDS", got.GetMetric())
	}
	if got.GetUnit() != hubv1.InsightUnit_INSIGHT_UNIT_SECONDS {
		t.Errorf("unit = %v, want SECONDS", got.GetUnit())
	}
	if got.GetBucketKey() != "" {
		t.Errorf("bucket_key = %q, want empty (only quota metrics carry one)", got.GetBucketKey())
	}
	// Oct 9: 6 idle hours = 21600 s behind two intervals; Oct 13: 8 h =
	// 28800 s behind one; Oct 6 has no intervals, so no data, never 0.
	pointValue(t, got, dayOf(9), 21600, 2)
	pointValue(t, got, dayOf(13), 28800, 1)
	pointValue(t, got, dayOf(6), 0, 0)
}

func TestSeries_EmptyRangeHasNoPoints(t *testing.T) {
	t.Parallel()
	got := QuotaUsedSeries(seriesAcct, "weekly_all", weekReadings(), seriesEnd, seriesEnd, hubv1.InsightGranularity_INSIGHT_GRANULARITY_DAY)
	if len(got.GetPoints()) != 0 {
		t.Errorf("points = %d, want 0 for an empty range", len(got.GetPoints()))
	}
}
