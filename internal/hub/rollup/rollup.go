package rollup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/protobuf/encoding/protojson"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/insights"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/store"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

const scope = "github.com/linuxdynasty/fleet-board/internal/hub/rollup"

// tableName is the `table` label of every rollup signal: the one table
// this pass writes.
const tableName = "insight_quota_window"

// defaultReserve is the quota kept for interactive use when an account
// has no stored spec (a new account gets 10, 05 section 3).
const defaultReserve = 10

// maxReserve caps a stored reserve the way pacing does (defaults.md: 0
// to 50).
const maxReserve = 50

// Options build a Roller. Telemetry is required.
type Options struct {
	// Now is the hub clock; nil means time.Now.
	Now       func() time.Time
	Telemetry *telemetry.Telemetry
}

// Roller runs the subscription-efficiency rollup: readings and reserves
// in, QuotaWindow rows out. One pass is one writer transaction; use it
// from the hub's single housekeeping goroutine.
type Roller struct {
	now      func() time.Time
	log      *slog.Logger
	tracer   trace.Tracer
	seconds  metric.Float64Histogram
	rows     metric.Int64Counter
	failures metric.Int64Counter
	lag      metric.Float64Histogram
}

// Result is one rollup pass: how many windows it wrote, for how many
// accounts, and the clock it stamped.
type Result struct {
	Windows   int
	Accounts  int
	Computed  time.Time
	DataStart time.Time
}

// New checks the options and registers the 05 section 9 signals.
func New(opts Options) (*Roller, error) {
	if opts.Telemetry == nil {
		return nil, errors.New("rollup: New needs telemetry")
	}
	r := &Roller{now: opts.Now, log: opts.Telemetry.Logger, tracer: opts.Telemetry.Tracer("rollup")}
	if r.now == nil {
		r.now = time.Now
	}
	m := opts.Telemetry.Meter(scope)
	p := opts.Telemetry.MetricPrefix()
	var err error
	if r.seconds, err = m.Float64Histogram(p+"_insights_rollup_seconds", metric.WithUnit("s"),
		metric.WithDescription("Subscription-efficiency rollup pass time by table.")); err != nil {
		return nil, fmt.Errorf("rollup: rollup seconds: %w", err)
	}
	if r.rows, err = m.Int64Counter(p+"_insights_rollup_rows",
		metric.WithDescription("Subscription-efficiency rollup rows written by table.")); err != nil {
		return nil, fmt.Errorf("rollup: rollup rows: %w", err)
	}
	if r.failures, err = m.Int64Counter(p+"_insights_rollup_failures_total",
		metric.WithDescription("Subscription-efficiency rollup passes that failed by table.")); err != nil {
		return nil, fmt.Errorf("rollup: rollup failures: %w", err)
	}
	if r.lag, err = m.Float64Histogram(p+"_insights_rollup_lag_seconds", metric.WithUnit("s"),
		metric.WithDescription("Staleness the subscription-efficiency rollup closed, by the pass's lag behind now.")); err != nil {
		return nil, fmt.Errorf("rollup: rollup lag: %w", err)
	}
	return r, nil
}

// errRebuildRange is a --from past now.
var errRebuildRange = errors.New("the rebuild start is past now")

// Rollup is the hourly pass: every window the stored readings imply is
// recomputed through insights.Windows. The first pass backfills history;
// later passes rewrite only windows ending yesterday or later (older
// windows are final). It stamps the rollup meta and records the 05
// section 9 signals.
func (r *Roller) Rollup(ctx context.Context, s *store.Store, now time.Time) (Result, error) {
	ctx, span := r.tracer.Start(ctx, "hub.insights.rollup")
	defer span.End()
	start := time.Now()
	prevComputed, prevStart, err := statedb.InsightRollupMeta(ctx, s)
	if err != nil {
		return r.fail(ctx, fmt.Errorf("rollup: read meta: %w", err))
	}
	inputs, err := Inputs(ctx, s, now)
	if err != nil {
		return r.fail(ctx, fmt.Errorf("rollup: read inputs: %w", err))
	}
	cutoff := time.Time{}
	if !prevStart.IsZero() {
		cutoff = dayStart(now).Add(-24 * time.Hour)
	}
	wins, accounts := buildWindows(inputs, now, cutoff)
	res, err := r.commit(ctx, s, wins, accounts, now, prevStart)
	if err != nil {
		return r.fail(ctx, err)
	}
	r.seconds.Record(ctx, time.Since(start).Seconds(), metric.WithAttributes(attribute.String("table", tableName)))
	r.rows.Add(ctx, int64(len(wins)), metric.WithAttributes(attribute.String("table", tableName)))
	if !prevComputed.IsZero() {
		r.lag.Record(ctx, now.Sub(prevComputed).Seconds(), metric.WithAttributes(attribute.String("table", tableName)))
	}
	r.log.InfoContext(ctx, "insights.rollup",
		"day", dayStart(now).Format(time.DateOnly), "rows", len(wins), "accounts", len(accounts))
	return res, nil
}

// Rebuild recomputes the windows ending at or after from from the full
// reading history (`fleetd insights rebuild --from`): older stored
// windows are untouched, so a gap heals without rewriting history. The
// trailing open window past now is always rewritten, so the current
// window stays fresh.
func (r *Roller) Rebuild(ctx context.Context, s *store.Store, from, now time.Time) (Result, error) {
	if from.After(now) {
		return Result{}, fmt.Errorf("rollup: rebuild needs from at or before now: %w", errRebuildRange)
	}
	ctx, span := r.tracer.Start(ctx, "hub.insights.rollup")
	defer span.End()
	start := time.Now()
	_, prevStart, err := statedb.InsightRollupMeta(ctx, s)
	if err != nil {
		return r.fail(ctx, fmt.Errorf("rollup: read meta: %w", err))
	}
	inputs, err := Inputs(ctx, s, now)
	if err != nil {
		return r.fail(ctx, fmt.Errorf("rollup: read inputs: %w", err))
	}
	wins, accounts := buildWindows(inputs, now, from)
	res, err := r.commit(ctx, s, wins, accounts, now, prevStart)
	if err != nil {
		return r.fail(ctx, err)
	}
	r.seconds.Record(ctx, time.Since(start).Seconds(), metric.WithAttributes(attribute.String("table", tableName)))
	r.rows.Add(ctx, int64(len(wins)), metric.WithAttributes(attribute.String("table", tableName)))
	r.log.InfoContext(ctx, "insights.rollup",
		"day", dayStart(now).Format(time.DateOnly), "rows", len(wins), "accounts", len(accounts))
	return res, nil
}

// fail counts one failed pass and returns the zero result with err.
func (r *Roller) fail(ctx context.Context, err error) (Result, error) {
	r.failures.Add(ctx, 1, metric.WithAttributes(attribute.String("table", tableName)))
	return Result{}, err
}

// buildWindows runs insights.Windows per account and keeps the windows
// ending at or after cutoff (the zero cutoff keeps everything: the
// first pass backfills history). It returns the stored rows plus every
// account with at least one window, kept or not.
func buildWindows(inputs []insights.AccountInput, now, cutoff time.Time) ([]statedb.InsightQuotaWindow, map[string]bool) {
	var wins []statedb.InsightQuotaWindow
	accounts := map[string]bool{}
	for _, in := range inputs {
		eff := insights.Windows(in)
		if len(eff.GetWindows()) > 0 {
			accounts[in.Account.GetValue()] = true
		}
		for _, w := range eff.GetWindows() {
			if !cutoff.IsZero() && w.GetEndTime().AsTime().Before(cutoff) {
				continue
			}
			raw, err := protojson.Marshal(w)
			if err != nil {
				continue
			}
			wins = append(wins, statedb.InsightQuotaWindow{
				AccountID: in.Account.GetValue(), BucketKey: w.GetBucketKey(),
				WindowEnd: w.GetEndTime().AsTime(), WindowJSON: raw, Updated: now,
			})
		}
	}
	sort.Slice(wins, func(i, j int) bool {
		if wins[i].AccountID != wins[j].AccountID {
			return wins[i].AccountID < wins[j].AccountID
		}
		if wins[i].BucketKey != wins[j].BucketKey {
			return wins[i].BucketKey < wins[j].BucketKey
		}
		return wins[i].WindowEnd.Before(wins[j].WindowEnd)
	})
	return wins, accounts
}

// commit writes one pass in a single writer transaction and stamps the
// rollup meta: computed is now, and the data start is the earliest
// window start this pass wrote (or the previous start when earlier).
// With no windows and no previous start there is no history yet, so the
// meta stays unset and GetInsights falls back to the live clock.
func (r *Roller) commit(ctx context.Context, s *store.Store, wins []statedb.InsightQuotaWindow,
	accounts map[string]bool, now, prevStart time.Time,
) (Result, error) {
	if len(wins) > 0 {
		if err := statedb.UpsertInsightQuotaWindows(ctx, s, wins); err != nil {
			return Result{}, fmt.Errorf("rollup: store windows: %w", err)
		}
	}
	start := prevStart
	for _, w := range wins {
		var q hubv1.QuotaWindow
		if err := protojson.Unmarshal(w.WindowJSON, &q); err != nil {
			continue
		}
		if st := q.GetStartTime().AsTime(); start.IsZero() || st.Before(start) {
			start = st
		}
	}
	res := Result{Windows: len(wins), Accounts: len(accounts), Computed: now, DataStart: start}
	if start.IsZero() {
		return res, nil
	}
	if err := statedb.SetInsightRollupMeta(ctx, s, now, start); err != nil {
		return Result{}, fmt.Errorf("rollup: store meta: %w", err)
	}
	return res, nil
}

// Inputs reads every stored usage reading and account, grouping the
// readings by account into one insights.AccountInput each. The reserve
// comes from the account spec (capped at 50 like pacing); an account
// with no stored spec reads the 10-point default. Pace intervals and
// goal spends have no stored rows yet, so they stay empty and the
// windows derived from them read 0. GetInsights builds the day in
// progress from the same inputs, so the served trailing window matches
// the rollup's by construction.
func Inputs(ctx context.Context, s *store.Store, now time.Time) ([]insights.AccountInput, error) {
	readings, err := statedb.ListStoredUsageReadings(ctx, s)
	if err != nil {
		return nil, fmt.Errorf("rollup: list usage readings: %w", err)
	}
	rows, err := statedb.ListAccountRows(ctx, s)
	if err != nil {
		return nil, fmt.Errorf("rollup: list accounts: %w", err)
	}
	reserves := map[string]uint32{}
	for _, row := range rows {
		reserves[row.ID] = reserveOf(row.Spec)
	}
	byAccount := map[string]*insights.AccountInput{}
	ordered := []string{}
	for _, stored := range readings {
		in, ok := byAccount[stored.AccountID]
		if !ok {
			reserve, ok := reserves[stored.AccountID]
			if !ok {
				reserve = defaultReserve
			}
			in = &insights.AccountInput{
				Account:        &typev1.AccountId{Value: stored.AccountID},
				ReservePercent: reserve,
				Now:            now,
			}
			byAccount[stored.AccountID] = in
			ordered = append(ordered, stored.AccountID)
		}
		mergeReading(in, stored)
	}
	sort.Strings(ordered)
	out := make([]insights.AccountInput, 0, len(ordered))
	for _, id := range ordered {
		out = append(out, *byAccount[id])
	}
	return out, nil
}

// mergeReading parses one stored reading's buckets into its account's
// input, one BucketReading per bucket the harness showed.
func mergeReading(in *insights.AccountInput, stored statedb.UsageReadingStored) {
	var raw []json.RawMessage
	if err := json.Unmarshal(stored.BucketsJSON, &raw); err != nil || len(raw) == 0 {
		return
	}
	readAt := time.UnixMilli(stored.ReadMs).UTC()
	goodParse := stored.Parse != "FAILED"
	for _, b := range raw {
		var bucket typev1.LimitBucket
		if err := protojson.Unmarshal(b, &bucket); err != nil {
			continue
		}
		used, known := percentValue(bucket.GetPercentUsed())
		reading := insights.BucketReading{Time: readAt, Used: used, Good: goodParse && known}
		if t := bucket.GetResetsTime().GetResolvedTime(); t != nil && t.IsValid() {
			reading.Resets, reading.HasReset = t.AsTime(), true
		}
		span, hasSpan := windowOf(&bucket)
		addBucket(in, bucket.GetKey(), bucket.GetKind(), span, hasSpan, reading)
	}
}

// windowOf returns the window length the harness showed for one bucket.
func windowOf(bucket *typev1.LimitBucket) (time.Duration, bool) {
	d := bucket.GetWindow()
	if d == nil || !d.IsValid() {
		return 0, false
	}
	if span := d.AsDuration(); span > 0 {
		return span, true
	}
	return 0, false
}

// addBucket appends reading to its bucket's history, opening the bucket
// entry on first sight with the window the harness showed.
func addBucket(in *insights.AccountInput, key string, kind typev1.WindowKind, span time.Duration, hasSpan bool, reading insights.BucketReading) {
	for i := range in.Buckets {
		if in.Buckets[i].Key == key {
			in.Buckets[i].Readings = append(in.Buckets[i].Readings, reading)
			return
		}
	}
	in.Buckets = append(in.Buckets, insights.BucketInput{
		Key: key, Kind: kind, Window: span, HasWindow: hasSpan,
		Readings: []insights.BucketReading{reading},
	})
}

// percentValue reads a known percent's points.
func percentValue(p *typev1.Percent) (float64, bool) {
	v, ok := p.GetKind().(*typev1.Percent_Value)
	if !ok {
		return 0, false
	}
	return float64(v.Value), true
}

// reserveOf reads the reserve from an account spec, capped at 50 like
// pacing; an unreadable spec reads the 10-point default.
func reserveOf(spec []byte) uint32 {
	var parsed hubv1.AccountSpec
	if err := protojson.Unmarshal(spec, &parsed); err != nil {
		return defaultReserve
	}
	if parsed.GetReservePercent() > maxReserve {
		return maxReserve
	}
	return parsed.GetReservePercent()
}

// dayStart truncates to the UTC day.
func dayStart(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}
