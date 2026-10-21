package api_test

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

var insightBase = time.Date(2026, time.October, 6, 0, 0, 0, 0, time.UTC)

// insightReading stores one noon reading of the weekly bucket: used runs
// 10 to 75 through the week, then a reset to 5 after the Monday reset.
func insightReading(t *testing.T, e *fleetEnv, at time.Time, used float64, resets time.Time) {
	t.Helper()
	bucket := &typev1.LimitBucket{
		Key:         "weekly_all",
		Kind:        typev1.WindowKind_WINDOW_KIND_WEEKLY,
		Window:      durationpb.New(7 * 24 * time.Hour),
		PercentUsed: &typev1.Percent{Kind: &typev1.Percent_Value{Value: uint32(used)}},
		ResetsTime:  &typev1.ResetTime{ResolvedTime: timestamppb.New(resets)},
	}
	raw, err := protojson.Marshal(bucket)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := statedb.InsertUsageReading(context.Background(), e.st, statedb.UsageSeed{
		AccountID: "acct-1", ReadTime: at, ReceivedTime: at.Add(time.Minute),
		AgentReadTime: at, Source: "PROBE", Parse: "OK",
		BucketsJSON: append(append([]byte("["), raw...), byte(']')),
	}); err != nil {
		t.Fatal(err)
	}
}

func seedInsightWeek(t *testing.T, e *fleetEnv) {
	t.Helper()
	for i, used := range []float64{10, 20, 30, 45, 55, 65, 75, 5} {
		resets := insightBase
		if i == 7 {
			resets = insightBase.Add(7 * 24 * time.Hour)
		}
		insightReading(t, e, time.Date(2026, time.October, 6+i, 12, 0, 0, 0, time.UTC), used, resets)
	}
}

func getInsights(t *testing.T, e *fleetEnv, req *hubv1.GetInsightsRequest) *hubv1.GetInsightsResponse {
	t.Helper()
	resp, err := e.client.GetInsights(context.Background(), connect.NewRequest(req))
	if err != nil {
		t.Fatalf("GetInsights: %v", err)
	}
	return resp.Msg
}

// TestGetInsightsDeniedPaths is the 02d READ gate: only the local user
// calls until row 12 brings caller scopes.
func TestGetInsightsDeniedPaths(t *testing.T) {
	t.Parallel()
	user := &typev1.Principal{
		Name: "bob", Role: typev1.Role_ROLE_OWNER, Kind: typev1.PrincipalKind_PRINCIPAL_KIND_USER,
		Id: &typev1.PrincipalId{Value: "01jabcdefghjkmnpqrstvwxyz0"},
	}
	for _, tc := range []struct {
		name   string
		who    *typev1.Principal
		code   connect.Code
		reason typev1.ReasonCode
	}{
		{"no principal", nil, connect.CodeUnauthenticated, typev1.ReasonCode_REASON_CODE_CREDENTIALS_INVALID},
		{"a signed-in user, before row 12 brings scopes", user, connect.CodePermissionDenied, typev1.ReasonCode_REASON_CODE_SCOPE_MISSING},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newFleetEnv(t, tc.who)
			_, err := e.client.GetInsights(context.Background(), connect.NewRequest(&hubv1.GetInsightsRequest{}))
			if code, reason, _ := detail(t, err); code != tc.code || reason != tc.reason {
				t.Errorf("GetInsights: got %v %v, want %v %v", code, reason, tc.code, tc.reason)
			}
		})
	}
}

// TestGetInsightsServesEfficiencyFromLiveReadings: with no rollup stored,
// the trailing window is computed from the source readings on the read
// pool, so the response is at most one hour behind (05 section 6).
// insightNow is after the first reset but before the second window ends,
// so the live recompute sees one complete week plus the trailing open
// window.
var insightNow = time.Date(2026, time.October, 14, 4, 0, 0, 0, time.UTC)

func TestGetInsightsServesEfficiencyFromLiveReadings(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), withNow(func() time.Time { return insightNow }))
	seedInsightWeek(t, e)
	msg := getInsights(t, e, &hubv1.GetInsightsRequest{
		Range: &hubv1.InsightRange{
			StartTime: timestamppb.New(insightBase),
			EndTime:   timestamppb.New(insightBase.Add(14 * 24 * time.Hour)),
		},
		Sections: []hubv1.InsightSection{hubv1.InsightSection_INSIGHT_SECTION_EFFICIENCY},
	})
	if len(msg.GetEfficiency()) != 1 {
		t.Fatalf("efficiency blocks = %d, want 1 account", len(msg.GetEfficiency()))
	}
	got := msg.GetEfficiency()[0]
	if got.GetAccount().GetValue() != "acct-1" {
		t.Errorf("account = %q, want acct-1", got.GetAccount().GetValue())
	}
	if len(got.GetWindows()) != 2 {
		t.Fatalf("windows = %d, want the complete week plus the trailing one", len(got.GetWindows()))
	}
	closed := got.GetWindows()[0]
	if got := closed.GetUsedAtEnd().GetValue(); got != 75 {
		t.Errorf("used_at_end = %d, want 75", got)
	}
	if got := closed.GetUsableUsed().GetValue(); got != 83 {
		t.Errorf("usable_used = %d, want 83 (75 of 90 usable)", got)
	}
	if len(got.GetSummaries()) != 1 {
		t.Fatalf("summaries = %d, want 1 bucket", len(got.GetSummaries()))
	}
	if got := got.GetSummaries()[0].GetWindows(); got != 1 {
		t.Errorf("summary windows = %d, want 1 complete window", got)
	}
	if msg.GetComputedTime() == nil || !msg.GetComputedTime().IsValid() {
		t.Error("computed_time unset, want the read time as fallback with no rollup")
	}
	// The served range is aligned to UTC days.
	if got := msg.GetRange().GetStartTime().AsTime(); !got.Equal(insightBase) {
		t.Errorf("range start = %v, want %v", got, insightBase)
	}
}

// TestGetInsightsServesStoredRollups: windows the hourly job stored are
// served as stored, with the rollup's computed_time and data_start_time.
func TestGetInsightsServesStoredRollups(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	computed := insightBase.Add(8 * 24 * time.Hour)
	if err := statedb.SetInsightRollupMeta(context.Background(), e.st, computed, insightBase); err != nil {
		t.Fatal(err)
	}
	mkWindow := func(start, end time.Time, complete bool, used uint32) []byte {
		w := &hubv1.QuotaWindow{
			BucketKey: "weekly_all",
			StartTime: timestamppb.New(start), EndTime: timestamppb.New(end),
			Complete: complete, ReservePercent: 10, Readings: 7,
			UsedAtEnd:  &typev1.Percent{Kind: &typev1.Percent_Value{Value: used}},
			UsableUsed: &typev1.Percent{Kind: &typev1.Percent_Value{Value: 83}},
			Unused:     &typev1.Percent{Kind: &typev1.Percent_Value{Value: 15}},
			FleetUsed:  &typev1.Percent{Kind: &typev1.Percent_Value{Value: 0}},
		}
		raw, err := protojson.Marshal(w)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	mid := insightBase.Add(7 * 24 * time.Hour)
	ctx := context.Background()
	for _, w := range []statedb.InsightQuotaWindow{
		{AccountID: "acct-1", BucketKey: "weekly_all", WindowEnd: mid, WindowJSON: mkWindow(insightBase, mid, true, 75), Updated: computed},
	} {
		if err := statedb.UpsertInsightQuotaWindow(ctx, e.st, w); err != nil {
			t.Fatal(err)
		}
	}
	msg := getInsights(t, e, &hubv1.GetInsightsRequest{
		Range: &hubv1.InsightRange{
			StartTime: timestamppb.New(insightBase),
			EndTime:   timestamppb.New(mid.Add(24 * time.Hour)),
		},
		Sections: []hubv1.InsightSection{hubv1.InsightSection_INSIGHT_SECTION_EFFICIENCY},
	})
	if len(msg.GetEfficiency()) != 1 || len(msg.GetEfficiency()[0].GetWindows()) == 0 {
		t.Fatalf("efficiency = %v, want the stored window", msg.GetEfficiency())
	}
	if got := msg.GetEfficiency()[0].GetWindows()[0].GetUsedAtEnd().GetValue(); got != 75 {
		t.Errorf("used_at_end = %d, want the stored 75", got)
	}
	if got := msg.GetComputedTime().AsTime(); !got.Equal(computed) {
		t.Errorf("computed_time = %v, want the rollup's %v", got, computed)
	}
	if got := msg.GetDataStartTime().AsTime(); !got.Equal(insightBase) {
		t.Errorf("data_start_time = %v, want %v", got, insightBase)
	}
}

// TestGetInsightsServesSeries plots the quota series from the readings
// and windows: used percent per day, waste at each reset, idle-behind
// seconds (0 with no pace rows stored yet).
func TestGetInsightsServesSeries(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), withNow(func() time.Time { return insightNow }))
	seedInsightWeek(t, e)
	msg := getInsights(t, e, &hubv1.GetInsightsRequest{
		Range: &hubv1.InsightRange{
			StartTime: timestamppb.New(insightBase),
			EndTime:   timestamppb.New(insightBase.Add(8 * 24 * time.Hour)),
		},
		Sections:    []hubv1.InsightSection{hubv1.InsightSection_INSIGHT_SECTION_SERIES},
		Granularity: hubv1.InsightGranularity_INSIGHT_GRANULARITY_DAY,
	})
	byMetric := map[hubv1.InsightMetric]*hubv1.InsightSeries{}
	for _, s := range msg.GetSeries() {
		byMetric[s.GetMetric()] = s
		if got := len(s.GetPoints()); got != 8 {
			t.Errorf("metric %v points = %d, want 8 daily bins", s.GetMetric(), got)
		}
	}
	for _, m := range []hubv1.InsightMetric{
		hubv1.InsightMetric_INSIGHT_METRIC_QUOTA_USED_PERCENT,
		hubv1.InsightMetric_INSIGHT_METRIC_QUOTA_UNUSED_PERCENT,
		hubv1.InsightMetric_INSIGHT_METRIC_IDLE_BEHIND_SECONDS,
	} {
		if byMetric[m] == nil {
			t.Errorf("no series for %v, want 1 (one account, one bucket)", m)
		}
	}
	// Oct 12 holds the last reading at 75; Oct 13 holds the complete
	// week's reset with 15 points of waste. Idle-behind carries no
	// value with no pace rows stored yet, never 0.
	if s := byMetric[hubv1.InsightMetric_INSIGHT_METRIC_QUOTA_USED_PERCENT]; s != nil {
		if got := s.GetPoints()[6].GetValue(); got != 75 {
			t.Errorf("used Oct 12 = %v, want 75", got)
		}
	}
	if s := byMetric[hubv1.InsightMetric_INSIGHT_METRIC_QUOTA_UNUSED_PERCENT]; s != nil {
		if got := s.GetPoints()[7].GetValue(); got != 15 {
			t.Errorf("unused Oct 13 = %v, want 15", got)
		}
	}
	if s := byMetric[hubv1.InsightMetric_INSIGHT_METRIC_IDLE_BEHIND_SECONDS]; s != nil {
		for i, pt := range s.GetPoints() {
			if pt.Value != nil {
				t.Errorf("idle_behind day %d = %v, want unset with no pace rows", i, pt.GetValue())
			}
		}
	}
}

// TestGetInsightsRefusesBadRanges: longer than 366 days and an end at or
// before the start are INVALID_ARGUMENT on the range.
func TestGetInsightsRefusesBadRanges(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	for _, tc := range []struct {
		name       string
		start, end time.Time
	}{
		{"a year and a day", insightBase, insightBase.Add(367 * 24 * time.Hour)},
		{"end before start", insightBase.Add(24 * time.Hour), insightBase},
		{"end at start", insightBase, insightBase},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := e.client.GetInsights(context.Background(), connect.NewRequest(&hubv1.GetInsightsRequest{
				Range: &hubv1.InsightRange{StartTime: timestamppb.New(tc.start), EndTime: timestamppb.New(tc.end)},
			}))
			if code, _, fields := detail(t, err); code != connect.CodeInvalidArgument {
				t.Errorf("GetInsights: code %v, want invalid_argument (fields %v)", code, fields)
			}
		})
	}
}

// TestGetInsightsUnbuiltSectionsStayUnimplemented pins the 02k release
// rule: OUTCOMES, STUCK and ROUTINES land in row 27, so asking for them
// answers UNIMPLEMENTED while EFFICIENCY and SERIES serve.
func TestGetInsightsUnbuiltSectionsStayUnimplemented(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	for _, s := range []hubv1.InsightSection{
		hubv1.InsightSection_INSIGHT_SECTION_OUTCOMES,
		hubv1.InsightSection_INSIGHT_SECTION_STUCK,
		hubv1.InsightSection_INSIGHT_SECTION_ROUTINES,
	} {
		_, err := e.client.GetInsights(context.Background(), connect.NewRequest(&hubv1.GetInsightsRequest{
			Sections: []hubv1.InsightSection{s},
		}))
		if code := connect.CodeOf(err); code != connect.CodeUnimplemented {
			t.Errorf("section %v: code %v, want unimplemented (row 27)", s, code)
		}
	}
}
