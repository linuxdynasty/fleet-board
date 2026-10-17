package insights

import (
	"math"
	"sort"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

const (
	// minWindow is the smallest quota window efficiency describes
	// (05 section 3): a 5-hour session bucket cannot "go to waste" in a
	// useful sense, so shorter buckets are skipped.
	minWindow = 24 * time.Hour
	// maxReservePercent bounds the reserve (defaults.md: 0 to 50), the
	// same bound pacing enforces, so the usable line matches dispatch.
	maxReservePercent = 50
	// endLookback is how far before a window's end its closing reading
	// may lie (05 section 3): used_at_end is unknown past 24 hours
	// without a good reading, never 0.
	endLookback = 24 * time.Hour
)

// BucketReading is one usage snapshot of one quota bucket: the percent
// used and the reset the harness showed. Good is false when parsing the
// reading failed, in which case Used and Resets carry nothing.
type BucketReading struct {
	Time     time.Time
	Used     float64
	Good     bool
	Resets   time.Time
	HasReset bool
}

// BucketInput is the reading history of one quota bucket of one account.
type BucketInput struct {
	Key       string
	Kind      typev1.WindowKind
	Window    time.Duration
	HasWindow bool
	Readings  []BucketReading
}

// PaceInterval is one stretch of pace history: whether the account was
// behind its burn-down pace, and whether a goal was running (05 section
// 3: idle_while_behind). IdleBehind counts only inside Behind.
type PaceInterval struct {
	Start      time.Time
	End        time.Time
	Behind     bool
	IdleBehind bool
}

// ReserveChange is the reserve in force from Time on (05 section 3: the
// usable line uses the reserve as it was when the window ended).
type ReserveChange struct {
	Time    time.Time
	Percent uint32
}

// FleetSpend is the quota points one goal run spent, ending at EndTime
// (GoalStatus.usage_consumed by the window each run ended in).
type FleetSpend struct {
	EndTime time.Time
	Points  float64
}

// AccountInput is everything Windows needs for one account: its reading
// history per bucket, its pace intervals, its goal spends, and the
// reserve history. Now is the instant the windows are judged at; zero
// means the current time.
type AccountInput struct {
	Account        *typev1.AccountId
	Buckets        []BucketInput
	Pace           []PaceInterval
	Spends         []FleetSpend
	ReservePercent uint32
	Reserves       []ReserveChange
	Now            time.Time
}

// Windows cuts one account's reading history into QuotaWindows (05
// section 3), oldest first, plus one EfficiencySummary per bucket that
// has a window. A window is found from the readings' resets: a jump of
// resets forward by at least half the window, or a drop of percent used
// (a silent reset starts the next window at the first reading that
// shows it). The trailing window ends at its last reset plus the window
// length and stays open until that passes.
func Windows(in AccountInput) *hubv1.AccountEfficiency {
	now := in.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	reserves := append([]ReserveChange(nil), in.Reserves...)
	sort.Slice(reserves, func(i, j int) bool { return reserves[i].Time.Before(reserves[j].Time) })
	spends := make([]FleetSpend, 0, len(in.Spends))
	for _, s := range in.Spends {
		if !s.EndTime.After(now) {
			spends = append(spends, s)
		}
	}
	out := &hubv1.AccountEfficiency{Account: in.Account}
	for _, b := range in.Buckets {
		wins := bucketWindows(b, in.Pace, spends, reserves, in.ReservePercent, now)
		if len(wins) == 0 {
			continue
		}
		out.Windows = append(out.Windows, wins...)
		out.Summaries = append(out.Summaries, Summarize(b.Key, wins))
	}
	return out
}

// bucketSpan is the window length efficiency uses for a bucket: the
// bucket's own window when it is at least a day, else the assumed 7
// days for a weekly bucket (the default pacing draws with). Anything
// else has no boundable window and is skipped.
func bucketSpan(b BucketInput) (time.Duration, bool) {
	if b.HasWindow && b.Window >= minWindow {
		return b.Window, true
	}
	if !b.HasWindow && b.Kind == typev1.WindowKind_WINDOW_KIND_WEEKLY {
		return 7 * 24 * time.Hour, true
	}
	return 0, false
}

// bucketWindows cuts one bucket's readings into windows. Readings past
// now are clock skew, never windows. Segments with no reset at all are
// tiled by the window length; once resets are observed the windows end
// at them, and readings past the trailing window's predicted end wait
// for the next observed reset.
func bucketWindows(b BucketInput, pace []PaceInterval, spends []FleetSpend, reserves []ReserveChange, reserve uint32, now time.Time) []*hubv1.QuotaWindow {
	span, ok := bucketSpan(b)
	if !ok || span <= 0 {
		return nil
	}
	readings := make([]BucketReading, 0, len(b.Readings))
	for _, r := range b.Readings {
		if !r.Time.After(now) {
			readings = append(readings, r)
		}
	}
	if len(readings) == 0 {
		return nil
	}
	sort.Slice(readings, func(i, j int) bool { return readings[i].Time.Before(readings[j].Time) })
	// bounds are the resets that open a window, in order: the first
	// reset observed opens the first window (an earlier reset only
	// closed a window with no readings in it).
	var bounds []time.Time
	var prevReset time.Time
	prevHas := false
	var prevUsed float64
	prevGood := false
	sawReset := false
	for _, r := range readings {
		var boundary time.Time
		switch {
		case r.HasReset && prevHas && r.Resets.Sub(prevReset) >= span/2:
			boundary = r.Resets
		case r.HasReset && !prevHas && len(bounds) == 0:
			boundary = r.Resets
		case r.Good && prevGood && r.Used < prevUsed:
			// The bucket reset without announcing: the earliest
			// evidence is this reading, so the next window starts
			// here.
			boundary = r.Time
		}
		if !boundary.IsZero() && (len(bounds) == 0 || boundary.After(bounds[len(bounds)-1])) {
			bounds = append(bounds, boundary)
		}
		if r.HasReset {
			prevReset, prevHas = r.Resets, true
			sawReset = true
		}
		if r.Good {
			prevUsed, prevGood = r.Used, true
		}
	}
	if !sawReset {
		return tiledWindows(b.Key, readings[0].Time, span, readings, pace, spends, reserves, reserve, now)
	}
	wins := make([]*hubv1.QuotaWindow, 0, len(bounds)+1)
	if readings[0].Time.Before(bounds[0]) {
		// Readings precede the first observed reset: they belong to
		// the window it closed, reported with partial data.
		wins = append(wins, buildWindow(b.Key, bounds[0].Add(-span), bounds[0], false, readings, pace, spends, reserves, reserve, now))
	}
	for i, start := range bounds {
		end := start.Add(span)
		trailing := true
		if i+1 < len(bounds) {
			end, trailing = bounds[i+1], false
		}
		wins = append(wins, buildWindow(b.Key, start, end, trailing, readings, pace, spends, reserves, reserve, now))
	}
	return wins
}

// tiledWindows covers readings that never showed a reset with
// window-length tiles from start; the trailing tile stays open.
func tiledWindows(key string, start time.Time, span time.Duration, readings []BucketReading, pace []PaceInterval, spends []FleetSpend, reserves []ReserveChange, reserve uint32, now time.Time) []*hubv1.QuotaWindow {
	var wins []*hubv1.QuotaWindow
	for s := start; !s.After(now); s = s.Add(span) {
		trailing := s.Add(span).After(now)
		wins = append(wins, buildWindow(key, s, s.Add(span), trailing, readings, pace, spends, reserves, reserve, now))
	}
	return wins
}

// buildWindow computes one QuotaWindow over [start, end): the last good
// reading in its final 24 hours sets used_at_end (unknown, never 0,
// past that), pace overlaps set behind and idle_behind, spends ending
// inside set fleet_used, and the reserve in force at the end sets the
// usable line. A reading exactly on a closed window's end opens the
// next window, so it never closes this one.
func buildWindow(key string, start, end time.Time, trailing bool, readings []BucketReading, pace []PaceInterval, spends []FleetSpend, reserves []ReserveChange, reserve uint32, now time.Time) *hubv1.QuotaWindow {
	lo := end.Add(-endLookback)
	var last *BucketReading
	failed := false
	for i := range readings {
		r := &readings[i]
		if r.Time.Before(lo) || r.Time.Before(start) {
			continue
		}
		if trailing {
			if r.Time.After(end) || r.Time.After(now) {
				continue
			}
		} else if !r.Time.Before(end) {
			continue
		}
		if r.Good {
			last = r
		} else {
			failed = true
		}
	}
	reserveEff := reserve
	for _, rc := range reserves {
		if !rc.Time.After(end) {
			reserveEff = rc.Percent
		}
	}
	if reserveEff > maxReservePercent {
		reserveEff = maxReservePercent
	}
	usable := 100 - reserveEff
	out := &hubv1.QuotaWindow{
		BucketKey:      key,
		StartTime:      timestamppb.New(start),
		EndTime:        timestamppb.New(end),
		Complete:       !now.Before(end),
		ReservePercent: reserveEff,
		Behind:         durationpb.New(overlap(pace, start, end, now, false)),
		IdleBehind:     durationpb.New(overlap(pace, start, end, now, true)),
	}
	for _, r := range readings {
		if !r.Time.Before(start) && r.Time.Before(end) {
			out.Readings++
		}
	}
	if last == nil {
		reason := typev1.UnknownReason_UNKNOWN_REASON_NOT_SHOWN
		if failed {
			// A parser failure at the end poisons the close: unknown,
			// not the last good value from days ago.
			reason = typev1.UnknownReason_UNKNOWN_REASON_PARSE_FAILED
		}
		out.UsedAtEnd = unknownPercent(reason)
		out.UsableUsed = unknownPercent(reason)
		out.Unused = unknownPercent(reason)
	} else {
		used := math.Min(math.Max(last.Used, 0), 100)
		out.UsedAtEnd = valuePercent(points(used))
		out.UsableUsed = valuePercent(points(math.Min(used, float64(usable)) / float64(usable) * 100))
		out.Unused = valuePercent(points(math.Max(float64(usable)-used, 0)))
	}
	out.FleetUsed = valuePercent(points(spentIn(spends, start, end)))
	return out
}

// overlap sums the pace intervals clipped to [start, end) and to now;
// idle sums only the part that was behind pace with no goal running.
func overlap(pace []PaceInterval, start, end, now time.Time, idle bool) time.Duration {
	var total time.Duration
	for _, p := range pace {
		if !p.Behind || (idle && !p.IdleBehind) {
			continue
		}
		s := maxTime(p.Start, start)
		e := minTime(minTime(p.End, end), now)
		if e.After(s) {
			total += e.Sub(s)
		}
	}
	return total
}

// spentIn sums the goal points whose run ended in [start, end).
func spentIn(spends []FleetSpend, start, end time.Time) float64 {
	var total float64
	for _, s := range spends {
		if !s.EndTime.Before(start) && s.EndTime.Before(end) {
			total += s.Points
		}
	}
	return total
}

// Summarize folds one bucket's windows into its EfficiencySummary (05
// section 3): the means run over the complete windows with a known
// value, the waste trend is the least-squares slope of unused over the
// last 8 complete windows (unset below 3), and idle_behind_total covers
// the complete windows. Windows of other buckets in the list are
// ignored.
func Summarize(bucketKey string, windows []*hubv1.QuotaWindow) *hubv1.EfficiencySummary {
	out := &hubv1.EfficiencySummary{BucketKey: bucketKey}
	complete := make([]*hubv1.QuotaWindow, 0, len(windows))
	for _, w := range windows {
		if w.GetBucketKey() == bucketKey && w.GetComplete() {
			complete = append(complete, w)
		}
	}
	sort.Slice(complete, func(i, j int) bool {
		return complete[i].GetEndTime().AsTime().Before(complete[j].GetEndTime().AsTime())
	})
	out.Windows = uint32(len(complete)) //nolint:gosec // G115: a bucket holds hundreds of windows at most
	var uuSum, unSum, idle float64
	var uuN, unN int
	unused := make([]float64, 0, len(complete))
	for _, w := range complete {
		if v, ok := percentKnown(w.GetUsableUsed()); ok {
			uuSum += v
			uuN++
		}
		if v, ok := percentKnown(w.GetUnused()); ok {
			unSum += v
			unN++
			unused = append(unused, v)
		}
		idle += w.GetIdleBehind().AsDuration().Seconds()
	}
	out.MeanUsableUsed = unknownPercent(typev1.UnknownReason_UNKNOWN_REASON_NOT_SHOWN)
	out.MeanUnused = unknownPercent(typev1.UnknownReason_UNKNOWN_REASON_NOT_SHOWN)
	if uuN > 0 {
		out.MeanUsableUsed = valuePercent(points(uuSum / float64(uuN)))
	}
	if unN > 0 {
		out.MeanUnused = valuePercent(points(unSum / float64(unN)))
	}
	if len(unused) >= 3 {
		if len(unused) > 8 {
			unused = unused[len(unused)-8:]
		}
		trend := int32(math.Round(slope(unused)))
		out.WasteTrendPoints = &trend
	}
	out.IdleBehindTotal = durationpb.New(time.Duration(idle * float64(time.Second)))
	return out
}

// slope is the least-squares slope of ys over x = 0..n-1, in points per
// window; positive means the waste is growing.
func slope(ys []float64) float64 {
	n := float64(len(ys))
	var sx, sy, sxx, sxy float64
	for i, y := range ys {
		x := float64(i)
		sx += x
		sy += y
		sxx += x * x
		sxy += x * y
	}
	den := n*sxx - sx*sx
	if den == 0 {
		return 0
	}
	return (n*sxy - sx*sy) / den
}

// percentKnown returns a known Percent's value.
func percentKnown(p *typev1.Percent) (float64, bool) {
	v, ok := p.GetKind().(*typev1.Percent_Value)
	if !ok {
		return 0, false
	}
	return float64(v.Value), true
}

// valuePercent wraps whole points; the Percent contract is 0..100.
func valuePercent(v uint32) *typev1.Percent {
	return &typev1.Percent{Kind: &typev1.Percent_Value{Value: v}}
}

// unknownPercent marks a percent the readings cannot set, with why.
func unknownPercent(reason typev1.UnknownReason) *typev1.Percent {
	return &typev1.Percent{Kind: &typev1.Percent_Unknown{Unknown: &typev1.PercentUnknown{Reason: reason}}}
}

// points rounds to whole points, capping past-full values the way
// pacing caps a bad reading: the Percent contract is 0..100.
func points(f float64) uint32 {
	if f <= 0 {
		return 0
	}
	if f = math.Round(f); f > 100 {
		return 100
	}
	return uint32(f)
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
