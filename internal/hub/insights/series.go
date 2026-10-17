package insights

import (
	"math"
	"sort"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// Series covers the quota metrics of GetInsights (the row-13 sections
// EFFICIENCY and SERIES): the last reading per bucket, the waste at
// each reset, and the idle-behind seconds. Goal metrics (GOALS_*,
// MERGE_RATE, NUDGES, QUOTA_POINTS_SPENT) belong to the OUTCOMES section
// and land with row 27, which owns the goal rollups.
//
// Every bin in [start, end) gets a point; bins with no data carry an
// unset value, never 0. The caller aligns the range to UTC days and
// keeps it within 366 days (the GetInsights contract).
func QuotaUsedSeries(account *typev1.AccountId, bucketKey string, readings []BucketReading, start, end time.Time, gran hubv1.InsightGranularity) *hubv1.InsightSeries {
	sorted := append([]BucketReading(nil), readings...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Time.Before(sorted[j].Time) })
	s := quotaSeries(account, bucketKey, hubv1.InsightMetric_INSIGHT_METRIC_QUOTA_USED_PERCENT, hubv1.InsightUnit_INSIGHT_UNIT_PERCENT)
	for _, bin := range bins(start, end, gran) {
		pt := &hubv1.InsightPoint{StartTime: timestamppb.New(bin[0])}
		var last *BucketReading
		var n uint32
		for i := range sorted {
			r := &sorted[i]
			if r.Time.Before(bin[0]) || !r.Time.Before(bin[1]) || !r.Good {
				continue
			}
			n++
			last = r
		}
		if last != nil {
			v := math.Min(math.Max(last.Used, 0), 100)
			pt.Value = &v
			pt.SampleSize = n
		}
		s.Points = append(s.Points, pt)
	}
	return s
}

// QuotaUnusedSeries plots the waste at each reset: one point per bin
// holding a complete window's end, unset elsewhere.
func QuotaUnusedSeries(account *typev1.AccountId, bucketKey string, windows []*hubv1.QuotaWindow, start, end time.Time, gran hubv1.InsightGranularity) *hubv1.InsightSeries {
	s := quotaSeries(account, bucketKey, hubv1.InsightMetric_INSIGHT_METRIC_QUOTA_UNUSED_PERCENT, hubv1.InsightUnit_INSIGHT_UNIT_PERCENT)
	for _, bin := range bins(start, end, gran) {
		pt := &hubv1.InsightPoint{StartTime: timestamppb.New(bin[0])}
		var v float64
		var n uint32
		for _, w := range windows {
			if w.GetBucketKey() != bucketKey || !w.GetComplete() {
				continue
			}
			uv, ok := percentKnown(w.GetUnused())
			if !ok {
				continue
			}
			t := w.GetEndTime().AsTime()
			if t.Before(bin[0]) || !t.Before(bin[1]) {
				continue
			}
			v, n = uv, n+1
		}
		if n > 0 {
			pt.Value = &v
			pt.SampleSize = n
		}
		s.Points = append(s.Points, pt)
	}
	return s
}

// IdleBehindSeries plots the seconds behind pace with no goal running
// per bin. A bin no pace interval touches carries an unset value (no
// data); a touched bin with no idle time reads 0.
func IdleBehindSeries(account *typev1.AccountId, pace []PaceInterval, start, end time.Time, gran hubv1.InsightGranularity) *hubv1.InsightSeries {
	s := &hubv1.InsightSeries{
		Metric: hubv1.InsightMetric_INSIGHT_METRIC_IDLE_BEHIND_SECONDS,
		Unit:   hubv1.InsightUnit_INSIGHT_UNIT_SECONDS,
		Group:  &hubv1.InsightGroupKey{Account: account},
	}
	for _, bin := range bins(start, end, gran) {
		pt := &hubv1.InsightPoint{StartTime: timestamppb.New(bin[0])}
		var seconds float64
		var n uint32
		for _, p := range pace {
			if !p.End.After(bin[0]) || !p.Start.Before(bin[1]) {
				continue
			}
			n++
			if p.Behind && p.IdleBehind {
				s0 := maxTime(p.Start, bin[0])
				e0 := minTime(p.End, bin[1])
				if e0.After(s0) {
					seconds += e0.Sub(s0).Seconds()
				}
			}
		}
		if n > 0 {
			pt.Value = &seconds
			pt.SampleSize = n
		}
		s.Points = append(s.Points, pt)
	}
	return s
}

func quotaSeries(account *typev1.AccountId, bucketKey string, metric hubv1.InsightMetric, unit hubv1.InsightUnit) *hubv1.InsightSeries {
	return &hubv1.InsightSeries{
		Metric:    metric,
		Unit:      unit,
		Group:     &hubv1.InsightGroupKey{Account: account},
		BucketKey: bucketKey,
	}
}

// bins splits [start, end) into day or week buckets: UTC days, or weeks
// starting Monday. An unspecified granularity defaults to days (weeks
// past 90 days, the GetInsights rule). The range edges are truncated
// down, so a trailing partial bucket still gets its point.
func bins(start, end time.Time, gran hubv1.InsightGranularity) [][2]time.Time {
	if !end.After(start) {
		return nil
	}
	g := gran
	switch g {
	case hubv1.InsightGranularity_INSIGHT_GRANULARITY_DAY, hubv1.InsightGranularity_INSIGHT_GRANULARITY_WEEK:
	default:
		g = hubv1.InsightGranularity_INSIGHT_GRANULARITY_DAY
		if end.Sub(start) > 90*24*time.Hour {
			g = hubv1.InsightGranularity_INSIGHT_GRANULARITY_WEEK
		}
	}
	trunc := dayBin
	step := 24 * time.Hour
	if g == hubv1.InsightGranularity_INSIGHT_GRANULARITY_WEEK {
		trunc = weekBin
		step = 7 * 24 * time.Hour
	}
	var out [][2]time.Time
	for b := trunc(start); b.Before(end); {
		nb := b.Add(step)
		if nb.After(end) {
			nb = end
		}
		out = append(out, [2]time.Time{b, nb})
		b = nb
	}
	return out
}

// dayBin truncates to the UTC day; weekBin to the Monday starting it.
func dayBin(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func weekBin(t time.Time) time.Time {
	d := dayBin(t)
	// Monday is 0: Sunday (0) shifts back 6, Monday (1) stays.
	shift := (int(d.Weekday()) + 6) % 7
	return d.AddDate(0, 0, -shift)
}
