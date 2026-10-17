package insights

import (
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

var (
	// The weekly bucket resets Tuesday Oct 6 at midnight UTC; windows
	// follow the resets, not the calendar week.
	weekReset = time.Date(2026, time.October, 6, 0, 0, 0, 0, time.UTC)
	weekNow   = time.Date(2026, time.October, 14, 0, 0, 0, 0, time.UTC)
)

// weekReadings is one weekly bucket read at noon every day: 10 to 75 points
// through the week, then a reset to 5 after the Monday reset.
func weekReadings() []BucketReading {
	used := []float64{10, 20, 30, 45, 55, 65, 75, 5}
	out := make([]BucketReading, 0, len(used))
	for i, u := range used {
		resets := weekReset
		if i == 7 {
			resets = weekReset.Add(7 * 24 * time.Hour)
		}
		out = append(out, BucketReading{
			Time:     time.Date(2026, time.October, 6+i, 12, 0, 0, 0, time.UTC),
			Used:     u,
			Good:     true,
			Resets:   resets,
			HasReset: true,
		})
	}
	return out
}

func weekInput() AccountInput {
	return AccountInput{
		Account:        &typev1.AccountId{Value: "acct-1"},
		ReservePercent: 10,
		Buckets: []BucketInput{{
			Key:       "weekly_all",
			Kind:      typev1.WindowKind_WINDOW_KIND_WEEKLY,
			Window:    7 * 24 * time.Hour,
			HasWindow: true,
			Readings:  weekReadings(),
		}},
		Pace: []PaceInterval{
			// 12 h behind, then 6 h behind and idle, both 2026-10-09.
			{
				Start:  time.Date(2026, time.October, 9, 0, 0, 0, 0, time.UTC),
				End:    time.Date(2026, time.October, 9, 12, 0, 0, 0, time.UTC),
				Behind: true,
			},
			{
				Start:  time.Date(2026, time.October, 9, 12, 0, 0, 0, time.UTC),
				End:    time.Date(2026, time.October, 9, 18, 0, 0, 0, time.UTC),
				Behind: true, IdleBehind: true,
			},
			// 8 h behind and idle in the trailing window.
			{
				Start:  time.Date(2026, time.October, 13, 12, 0, 0, 0, time.UTC),
				End:    time.Date(2026, time.October, 13, 20, 0, 0, 0, time.UTC),
				Behind: true, IdleBehind: true,
			},
		},
		Spends: []FleetSpend{
			{EndTime: time.Date(2026, time.October, 8, 15, 0, 0, 0, time.UTC), Points: 12.4},
			{EndTime: time.Date(2026, time.October, 11, 9, 0, 0, 0, time.UTC), Points: 3.2},
			{EndTime: time.Date(2026, time.October, 13, 14, 0, 0, 0, time.UTC), Points: 2.0},
		},
		Now: weekNow,
	}
}

// TestWindows_HandComputedWeek is the #41 acceptance line "rollup equals a
// hand-computed week": every number below was worked out by hand in the
// test comment, never by running the code.
func TestWindows_HandComputedWeek(t *testing.T) {
	t.Parallel()
	got := Windows(weekInput())
	if got == nil {
		t.Fatal("Windows returned nil")
	}
	wins := got.GetWindows()
	if len(wins) != 2 {
		t.Fatalf("windows = %d, want 2 (one complete week plus the trailing one)", len(wins))
	}
	// Window 1: [Oct 6, Oct 13), complete. Last good reading in the final
	// 24 h is Oct 12 noon at 75; usable is 100-10=90, so usable_used is
	// 75/90 = 83.33 -> 83 and waste is 90-75 = 15. Seven readings fall in
	// the window; Oct 13 noon belongs to the next one. Behind is 12 h + 6 h
	// = 18 h with 6 h idle; fleet points are 12.4 + 3.2 = 15.6 -> 16.
	closed := wins[0]
	if !closed.GetComplete() {
		t.Error("window 1 complete = false, want true (its reset passed)")
	}
	if got := closed.GetStartTime().AsTime(); !got.Equal(weekReset) {
		t.Errorf("window 1 start = %v, want %v", got, weekReset)
	}
	if got := closed.GetEndTime().AsTime(); !got.Equal(weekReset.Add(7 * 24 * time.Hour)) {
		t.Errorf("window 1 end = %v, want Oct 13", got)
	}
	if got := closed.GetUsedAtEnd().GetValue(); got != 75 {
		t.Errorf("used_at_end = %d, want 75", got)
	}
	if got := closed.GetUsableUsed().GetValue(); got != 83 {
		t.Errorf("usable_used = %d, want 83 (75 of 90 usable)", got)
	}
	if got := closed.GetUnused().GetValue(); got != 15 {
		t.Errorf("unused = %d, want 15", got)
	}
	if got := closed.GetFleetUsed().GetValue(); got != 16 {
		t.Errorf("fleet_used = %d, want 16 (12.4 + 3.2 rounded)", got)
	}
	if got := closed.GetBehind().AsDuration(); got != 18*time.Hour {
		t.Errorf("behind = %v, want 18h", got)
	}
	if got := closed.GetIdleBehind().AsDuration(); got != 6*time.Hour {
		t.Errorf("idle_behind = %v, want 6h", got)
	}
	if got := closed.GetReadings(); got != 7 {
		t.Errorf("readings = %d, want 7", got)
	}
	if got := closed.GetReservePercent(); got != 10 {
		t.Errorf("reserve_percent = %d, want 10", got)
	}
	// Window 2: [Oct 13, Oct 20), still open. No reading falls in its final
	// 24 h yet, so every percent is unknown, never 0.
	open := wins[1]
	if open.GetComplete() {
		t.Error("window 2 complete = true, want false (its reset is Oct 20)")
	}
	if open.GetUsedAtEnd().GetKind() == nil {
		t.Error("window 2 used_at_end has no kind, want unknown")
	}
	if _, ok := open.GetUsedAtEnd().GetKind().(*typev1.Percent_Unknown); !ok {
		t.Errorf("window 2 used_at_end kind = %T, want unknown", open.GetUsedAtEnd().GetKind())
	}
	if _, ok := open.GetUsableUsed().GetKind().(*typev1.Percent_Unknown); !ok {
		t.Error("window 2 usable_used is known, want unknown with no used_at_end")
	}
	if _, ok := open.GetUnused().GetKind().(*typev1.Percent_Unknown); !ok {
		t.Error("window 2 unused is known, want unknown with no used_at_end")
	}
	if got := open.GetFleetUsed().GetValue(); got != 2 {
		t.Errorf("window 2 fleet_used = %d, want 2", got)
	}
	if got := open.GetBehind().AsDuration(); got != 8*time.Hour {
		t.Errorf("window 2 behind = %v, want 8h", got)
	}
	if got := open.GetReadings(); got != 1 {
		t.Errorf("window 2 readings = %d, want 1", got)
	}
}

func TestWindows_SkipsBucketsWithoutAUsableWindow(t *testing.T) {
	t.Parallel()
	in := weekInput()
	in.Buckets = append(in.Buckets,
		BucketInput{ // a 5-hour session bucket: too short to waste.
			Key:       "session",
			Kind:      typev1.WindowKind_WINDOW_KIND_SHORT,
			Window:    5 * time.Hour,
			HasWindow: true,
			Readings:  weekReadings(),
		},
		BucketInput{ // a monthly bucket with no window: unbounded.
			Key:      "monthly_included",
			Kind:     typev1.WindowKind_WINDOW_KIND_MONTHLY,
			Readings: weekReadings(),
		})
	got := Windows(in)
	for _, w := range got.GetWindows() {
		if w.GetBucketKey() != "weekly_all" {
			t.Errorf("window for bucket %q, want only weekly_all", w.GetBucketKey())
		}
	}
}

func TestWindows_ParseFailureAtEndIsUnknownNotZero(t *testing.T) {
	t.Parallel()
	in := weekInput()
	// The Oct 12 reading fails to parse, so the last good reading in the
	// final 24 h is gone; Oct 13 noon belongs to the next window.
	in.Buckets[0].Readings[6].Good = false
	got := Windows(in)
	closed := got.GetWindows()[0]
	if _, ok := closed.GetUsedAtEnd().GetKind().(*typev1.Percent_Unknown); !ok {
		t.Errorf("used_at_end kind = %T, want unknown after the end failure", closed.GetUsedAtEnd().GetKind())
	}
	if got := closed.GetReadings(); got != 7 {
		t.Errorf("readings = %d, want 7 (the failed probe still read)", got)
	}
}

func TestWindows_UsedDropStartsANewWindow(t *testing.T) {
	t.Parallel()
	in := weekInput()
	// Same resets_time throughout, but usage falls off a cliff on Oct 10:
	// the weekly bucket reset without the harness saying so.
	in.Buckets[0].Readings[4].Used = 4
	got := Windows(in)
	wins := got.GetWindows()
	if len(wins) != 3 {
		t.Fatalf("windows = %d, want 3 (the drop splits the first week)", len(wins))
	}
	if got := wins[0].GetEndTime().AsTime(); !got.Equal(in.Buckets[0].Readings[4].Time) {
		t.Errorf("first window end = %v, want the Oct 10 noon reading time", got)
	}
	if got := wins[0].GetUsedAtEnd().GetValue(); got != 45 {
		t.Errorf("first window used_at_end = %d, want 45 (Oct 9 noon)", got)
	}
}

func TestWindows_ReserveClampedToFifty(t *testing.T) {
	t.Parallel()
	in := weekInput()
	in.ReservePercent = 60
	got := Windows(in)
	closed := got.GetWindows()[0]
	if got := closed.GetReservePercent(); got != 50 {
		t.Errorf("reserve_percent = %d, want 50 (clamped, pacing maxReservePercent)", got)
	}
	// Usable is 50, used 75 is past it: usable_used caps at 100, waste is 0.
	if got := closed.GetUsableUsed().GetValue(); got != 100 {
		t.Errorf("usable_used = %d, want 100 (capped)", got)
	}
	if got := closed.GetUnused().GetValue(); got != 0 {
		t.Errorf("unused = %d, want 0 (use past the usable line is not negative waste)", got)
	}
}

func TestWindows_FutureReadingsIgnored(t *testing.T) {
	t.Parallel()
	in := weekInput()
	in.Buckets[0].Readings = append(in.Buckets[0].Readings, BucketReading{
		Time:     weekNow.Add(2 * time.Hour),
		Used:     99,
		Good:     true,
		Resets:   weekReset.Add(7 * 24 * time.Hour),
		HasReset: true,
	})
	got := Windows(in)
	if len(got.GetWindows()) != 2 {
		t.Fatalf("windows = %d, want 2 (a future reading opens nothing)", len(got.GetWindows()))
	}
	if got := got.GetWindows()[1].GetReadings(); got != 1 {
		t.Errorf("trailing readings = %d, want 1 (future reading ignored)", got)
	}
}

// summaryWindows builds complete windows with the given waste points and
// matching usable_used values for Summarize tests.
func summaryWindows(unused ...uint32) []*hubv1.QuotaWindow {
	usable := []uint32{83, 80, 78, 75}
	out := make([]*hubv1.QuotaWindow, 0, len(unused))
	for i, u := range unused {
		uu := usable[i%len(usable)]
		out = append(out, &hubv1.QuotaWindow{
			BucketKey:  "weekly_all",
			Complete:   true,
			UsableUsed: &typev1.Percent{Kind: &typev1.Percent_Value{Value: uu}},
			Unused:     &typev1.Percent{Kind: &typev1.Percent_Value{Value: u}},
		})
	}
	return out
}

func TestSummarize_MeansAndWasteTrend(t *testing.T) {
	t.Parallel()
	// Waste 14, 20, 24, 30: mean 22; slope over x = 0..3 is
	// (632 - 528) / (56 - 36) = 5.2, rounded to 5 points per window.
	// Usable 83, 80, 78, 75 averages to exactly 79.
	got := Summarize("weekly_all", summaryWindows(14, 20, 24, 30))
	trend := int32(5)
	want := &hubv1.EfficiencySummary{
		BucketKey:        "weekly_all",
		Windows:          4,
		MeanUsableUsed:   valuePercent(79),
		MeanUnused:       valuePercent(22),
		WasteTrendPoints: &trend,
		IdleBehindTotal:  durationpb.New(0),
	}
	if !proto.Equal(got, want) {
		t.Errorf("summary = %v, want %v", got, want)
	}
}

func TestSummarize_TrendUnsetBelowThreeWindows(t *testing.T) {
	t.Parallel()
	// Usable (83 + 80) / 2 = 81.5 -> 82; waste (14 + 20) / 2 = 17.
	got := Summarize("weekly_all", summaryWindows(14, 20))
	want := &hubv1.EfficiencySummary{
		BucketKey:       "weekly_all",
		Windows:         2,
		MeanUsableUsed:  valuePercent(82),
		MeanUnused:      valuePercent(17),
		IdleBehindTotal: durationpb.New(0),
	}
	if !proto.Equal(got, want) {
		t.Errorf("summary = %v, want %v", got, want)
	}
}

func TestSummarize_UnknownWindowsDoNotMoveTheMeans(t *testing.T) {
	t.Parallel()
	// Waste (14 + 20 + 24) / 3 = 19.33 -> 19; usable
	// (83 + 80 + 78) / 3 = 80.33 -> 80; trend over the three known is
	// (204 - 174) / (15 - 9) = 5. The unknown window counts in windows
	// but not in the means.
	wins := summaryWindows(14, 20, 24)
	wins = append(wins, &hubv1.QuotaWindow{BucketKey: "weekly_all", Complete: true})
	got := Summarize("weekly_all", wins)
	trend := int32(5)
	want := &hubv1.EfficiencySummary{
		BucketKey:        "weekly_all",
		Windows:          4,
		MeanUsableUsed:   valuePercent(80),
		MeanUnused:       valuePercent(19),
		WasteTrendPoints: &trend,
		IdleBehindTotal:  durationpb.New(0),
	}
	if !proto.Equal(got, want) {
		t.Errorf("summary = %v, want %v", got, want)
	}
}
