package api

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"connectrpc.com/connect"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/insights"
	"github.com/linuxdynasty/fleet-board/internal/hub/rollup"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// Insight sections this row serves (02k): subscription efficiency and
// its series. OUTCOMES, STUCK and ROUTINES land with row 27; asking for
// them answers UNIMPLEMENTED so a view knows to wait for that row.
const (
	maxInsightDays   = 366
	maxInsightSeries = 200
	insightPadDays   = 32
)

// authorizeInsights admits the checked admin user or local TCP owner for
// GetInsights (02d: READ).
func authorizeInsights(ctx context.Context) (*typev1.Principal, error) {
	return authorizeScope(ctx, "READ")
}

// GetInsights serves subscription efficiency and its quota series from
// the hourly rollup rows (05 section 6). The range defaults to the last
// 30 days, aligns down to UTC days and caps at 366 days; the day in
// progress is recomputed from the source readings on the read pool, so a
// response is at most one hour behind the rollups. Stored complete
// windows are served as stored; the open window always comes from the
// live recompute. Pace history and goal spends have no stored rows yet,
// so behind, idle_behind and fleet_used read 0 until their rows land.
func (f *Fleet) GetInsights(ctx context.Context, req *connect.Request[hubv1.GetInsightsRequest],
) (*connect.Response[hubv1.GetInsightsResponse], error) {
	who, err := authorizeInsights(ctx)
	if err != nil {
		return nil, f.insightsRefused(ctx, err)
	}
	actor := who.GetId().GetValue()
	eff, series, err := validatedSections(req.Msg.GetSections())
	if err != nil {
		return nil, f.insightsRefused(ctx, err)
	}
	now := f.hubNow()
	start, end, err := alignedRange(req.Msg.GetRange(), now)
	if err != nil {
		return nil, f.insightsRefused(ctx, err)
	}
	if err := quotaCheckpoint(ctx, quotaRead); err != nil {
		return nil, err
	}
	msg, step, err := f.buildInsights(ctx, req.Msg, eff, series, start, end, now)
	if err != nil {
		return nil, f.insightsFailed(ctx, actor, step, err)
	}
	return connect.NewResponse(msg), nil
}

// validatedSections splits the requested sections into the two this row
// serves. Empty means both. An unbuilt section (OUTCOMES, STUCK,
// ROUTINES) is UNIMPLEMENTED with the row that builds it; an unknown
// section value is INVALID_ARGUMENT.
func validatedSections(sections []hubv1.InsightSection) (eff, series bool, err error) {
	if len(sections) == 0 {
		return true, true, nil
	}
	for _, s := range sections {
		switch s {
		case hubv1.InsightSection_INSIGHT_SECTION_EFFICIENCY:
			eff = true
		case hubv1.InsightSection_INSIGHT_SECTION_SERIES:
			series = true
		case hubv1.InsightSection_INSIGHT_SECTION_OUTCOMES,
			hubv1.InsightSection_INSIGHT_SECTION_STUCK,
			hubv1.InsightSection_INSIGHT_SECTION_ROUTINES:
			return false, false, connect.NewError(connect.CodeUnimplemented,
				fmt.Errorf("api: GetInsights section %v lands in row 27", s))
		default:
			return false, false, apiError(connect.CodeInvalidArgument,
				typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
				fmt.Sprintf("unknown insight section %v", s), nil,
				[]*typev1.FieldViolation{{Field: "sections", Description: fmt.Sprintf("unknown insight section %v", s)}})
		}
	}
	return eff, series, nil
}

// alignedRange resolves the requested range: the default is the last 30
// days through today, edges align down to UTC days, and the span caps at
// 366 days (05 section 6). The step names the failure for the status log
// (range, store, decode).
func alignedRange(r *hubv1.InsightRange, now time.Time) (time.Time, time.Time, error) {
	endDay := dayOf(now).Add(24 * time.Hour)
	startDay := endDay.Add(-30 * 24 * time.Hour)
	if r != nil {
		if t := r.GetStartTime(); t != nil && t.IsValid() {
			startDay = dayOf(t.AsTime())
		}
		if t := r.GetEndTime(); t != nil && t.IsValid() {
			endDay = dayOf(t.AsTime())
		}
	}
	if !endDay.After(startDay) {
		return time.Time{}, time.Time{}, apiError(connect.CodeInvalidArgument,
			typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"the insight range ends at or before it starts", nil,
			[]*typev1.FieldViolation{{Field: "range", Description: "the insight range ends at or before it starts"}})
	}
	if endDay.Sub(startDay) > maxInsightDays*24*time.Hour {
		return time.Time{}, time.Time{}, apiError(connect.CodeInvalidArgument,
			typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			fmt.Sprintf("the insight range spans more than %d days", maxInsightDays), nil,
			[]*typev1.FieldViolation{{Field: "range", Description: "the insight range spans more than 366 days"}})
	}
	return startDay, endDay, nil
}

// buildInsights assembles the response over the stored rollup rows plus
// the live recompute of the windows in progress.
func (f *Fleet) buildInsights(ctx context.Context, req *hubv1.GetInsightsRequest, eff, series bool,
	start, end, now time.Time,
) (*hubv1.GetInsightsResponse, string, error) {
	born := time.Now()
	stored, err := f.storedWindows(ctx, start, end)
	if err != nil {
		return nil, "store", err
	}
	inputs, err := rollup.Inputs(ctx, f.store, now)
	if err != nil {
		return nil, "store", err
	}
	merged := mergeWindows(stored, liveWindows(inputs), start, end)
	out := &hubv1.GetInsightsResponse{
		Range: &hubv1.InsightRange{StartTime: timestamppb.New(start), EndTime: timestamppb.New(end)},
	}
	if eff {
		out.Efficiency = efficiencyBlocks(merged)
		f.observeInsightQuery(ctx, "EFFICIENCY", time.Since(born))
	}
	if series {
		out.Series, out.Truncated = insightSeries(inputs, merged, start, end, req.GetGranularity())
		f.observeInsightQuery(ctx, "SERIES", time.Since(born))
	}
	computed, dataStart, err := statedb.InsightRollupMeta(ctx, f.store)
	if err != nil {
		return nil, "store", err
	}
	if computed.IsZero() {
		computed = now
	}
	out.ComputedTime = timestamppb.New(computed)
	if !dataStart.IsZero() {
		out.DataStartTime = timestamppb.New(dataStart)
	}
	live, err := f.goalsLive(ctx, start, end)
	if err != nil {
		return nil, "store", err
	}
	out.GoalsLive = live
	f.tel.Logger.InfoContext(ctx, "insights.queried",
		"efficiency", len(out.GetEfficiency()), "series", len(out.GetSeries()), "goals_live", live)
	return out, "", nil
}

// accountWindow is one quota window with the account it describes: the
// window proto carries no account, so the rollup row (stored) or the
// input (live) supplies it.
type accountWindow struct {
	account string
	win     *hubv1.QuotaWindow
}

// storedWindows reads the rollup rows overlapping [start, end): the
// query pads by a month each side since a window ending inside the range
// may start before it. Rows that do not parse are a store failure, not
// a skipped row.
func (f *Fleet) storedWindows(ctx context.Context, start, end time.Time) ([]accountWindow, error) {
	rows, err := statedb.ListInsightQuotaWindows(ctx, f.store, "",
		start.Add(-insightPadDays*24*time.Hour), end.Add(insightPadDays*24*time.Hour))
	if err != nil {
		return nil, err
	}
	var out []accountWindow
	for _, r := range rows {
		var w hubv1.QuotaWindow
		if err := protojson.Unmarshal(r.WindowJSON, &w); err != nil {
			return nil, fmt.Errorf("api: decode stored quota window: %w", err)
		}
		out = append(out, accountWindow{account: r.AccountID, win: &w})
	}
	return out, nil
}

// liveWindows recomputes every window the stored readings imply: the day
// in progress comes from source rows, never from the rollup (05 section
// 6).
func liveWindows(inputs []insights.AccountInput) []accountWindow {
	var out []accountWindow
	for _, in := range inputs {
		for _, w := range insights.Windows(in).GetWindows() {
			out = append(out, accountWindow{account: in.Account.GetValue(), win: w})
		}
	}
	return out
}

// windowKey groups windows by the account and bucket they describe.
type windowKey struct {
	account, bucket string
}

// mergeWindows serves stored complete windows as stored and takes every
// newer window from the live recompute: the open window and any window
// the rollup has not closed since a reset. Only windows overlapping
// [start, end) are kept, oldest first.
func mergeWindows(stored, live []accountWindow, start, end time.Time) []accountWindow {
	completeEnd := map[windowKey]time.Time{}
	for _, w := range stored {
		if !w.win.GetComplete() {
			continue
		}
		k := windowKey{w.account, w.win.GetBucketKey()}
		if e := w.win.GetEndTime().AsTime(); e.After(completeEnd[k]) {
			completeEnd[k] = e
		}
	}
	var out []accountWindow
	keep := func(w *hubv1.QuotaWindow) bool {
		return w.GetStartTime().AsTime().Before(end) && w.GetEndTime().AsTime().After(start)
	}
	for _, w := range stored {
		if w.win.GetComplete() && keep(w.win) {
			out = append(out, w)
		}
	}
	for _, w := range live {
		k := windowKey{w.account, w.win.GetBucketKey()}
		if !w.win.GetEndTime().AsTime().After(completeEnd[k]) {
			continue
		}
		if keep(w.win) {
			out = append(out, w)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].account != out[j].account {
			return out[i].account < out[j].account
		}
		if out[i].win.GetBucketKey() != out[j].win.GetBucketKey() {
			return out[i].win.GetBucketKey() < out[j].win.GetBucketKey()
		}
		return out[i].win.GetStartTime().AsTime().Before(out[j].win.GetStartTime().AsTime())
	})
	return out
}

// efficiencyBlocks groups merged windows by account into one
// AccountEfficiency each with one EfficiencySummary per bucket.
func efficiencyBlocks(windows []accountWindow) []*hubv1.AccountEfficiency {
	byAccount := map[string][]*hubv1.QuotaWindow{}
	ordered := []string{}
	for _, w := range windows {
		id := w.account
		if _, ok := byAccount[id]; !ok {
			ordered = append(ordered, id)
		}
		byAccount[id] = append(byAccount[id], w.win)
	}
	sort.Strings(ordered)
	out := make([]*hubv1.AccountEfficiency, 0, len(ordered))
	for _, id := range ordered {
		wins := byAccount[id]
		buckets := map[string]bool{}
		for _, w := range wins {
			buckets[w.GetBucketKey()] = true
		}
		names := make([]string, 0, len(buckets))
		for b := range buckets {
			names = append(names, b)
		}
		sort.Strings(names)
		block := &hubv1.AccountEfficiency{Account: &typev1.AccountId{Value: id}, Windows: wins}
		for _, b := range names {
			block.Summaries = append(block.Summaries, insights.Summarize(b, wins))
		}
		out = append(out, block)
	}
	return out
}

// insightSeries plots one USED, one UNUSED and one IDLE_BEHIND series
// per account and bucket from its readings and merged windows, oldest
// group first, capped at 200 series (05: rows are ordered by their
// impact and capped).
func insightSeries(inputs []insights.AccountInput, merged []accountWindow,
	start, end time.Time, gran hubv1.InsightGranularity,
) ([]*hubv1.InsightSeries, bool) {
	byKey := map[windowKey][]*hubv1.QuotaWindow{}
	for _, w := range merged {
		k := windowKey{w.account, w.win.GetBucketKey()}
		byKey[k] = append(byKey[k], w.win)
	}
	var out []*hubv1.InsightSeries
	truncated := false
	for _, in := range inputs {
		buckets := append([]insights.BucketInput(nil), in.Buckets...)
		sort.Slice(buckets, func(i, j int) bool { return buckets[i].Key < buckets[j].Key })
		for _, b := range buckets {
			k := windowKey{in.Account.GetValue(), b.Key}
			for _, s := range []*hubv1.InsightSeries{
				insights.QuotaUsedSeries(in.Account, b.Key, b.Readings, start, end, gran),
				insights.QuotaUnusedSeries(in.Account, b.Key, byKey[k], start, end, gran),
				insights.IdleBehindSeries(in.Account, in.Pace, start, end, gran),
			} {
				if len(out) >= maxInsightSeries {
					return out, true
				}
				out = append(out, s)
			}
		}
	}
	return out, truncated
}

// goalsLive counts the goals that started in [start, end) and are still
// open, so a rate over a short range is read with them in mind.
func (f *Fleet) goalsLive(ctx context.Context, start, end time.Time) (uint32, error) {
	rows, err := statedb.ListGoalRows(ctx, f.store)
	if err != nil {
		return 0, err
	}
	var n uint32
	for _, g := range rows {
		at := time.UnixMilli(g.StartMs)
		if (at.Equal(start) || at.After(start)) && at.Before(end) && g.EndMs == nil {
			n++
		}
	}
	return n, nil
}

// dayOf truncates to the UTC day.
func dayOf(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// insightQueryCounters holds one fleet_hub_insights_query_seconds per
// Fleet, built on first use. The histogram lives here and not in
// NewFleet so this slice stays out of the constructor rows the unmerged
// row-12 slices edit.
var insightQueryCounters sync.Map // *Fleet -> metric.Float64Histogram

func (f *Fleet) observeInsightQuery(ctx context.Context, section string, took time.Duration) {
	var h metric.Float64Histogram
	if v, ok := insightQueryCounters.Load(f); ok {
		h = v.(metric.Float64Histogram) //nolint:forcetypeassert // only histograms are stored
	} else {
		built, err := f.tel.Meter(scope).Float64Histogram(f.tel.MetricPrefix()+"_insights_query_seconds",
			metric.WithUnit("s"),
			metric.WithDescription("GetInsights query time by section."))
		if err != nil {
			return
		}
		actual, _ := insightQueryCounters.LoadOrStore(f, built)
		h = actual.(metric.Float64Histogram) //nolint:forcetypeassert // only histograms are stored
	}
	h.Record(ctx, took.Seconds(), metric.WithAttributes(attribute.String("section", section)))
}

// insightsRefused logs an insights caller error by its reason and
// returns it.
func (f *Fleet) insightsRefused(ctx context.Context, err error) error {
	f.tel.Logger.InfoContext(ctx, "insights.query_refused", "reason", reasonOf(err))
	return err
}

// insightsFailed logs the hub's own insights failure at step (a closed
// set: range, store, decode) and returns INTERNAL without the cause's
// text.
func (f *Fleet) insightsFailed(ctx context.Context, actor, step string, cause error) error {
	kind := "io"
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		kind = "timeout"
	}
	f.tel.Logger.ErrorContext(ctx, "insights.query_failed", "step", step, "err_kind", kind, "actor", actor)
	return apiError(connect.CodeInternal, typev1.ReasonCode_REASON_CODE_INTERNAL,
		"the hub could not read its insights", nil, nil)
}
