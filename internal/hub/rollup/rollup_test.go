package rollup_test

import (
	"context"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/rollup"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/store"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

var (
	weekReset = time.Date(2026, time.October, 6, 0, 0, 0, 0, time.UTC)
	weekNow   = time.Date(2026, time.October, 14, 0, 0, 0, 0, time.UTC)
)

// testStore builds a fresh state.db for one test.
func testStore(t *testing.T) *store.Store {
	t.Helper()
	dir := t.TempDir() + "/state"
	if err := statedb.EnsureDir(dir); err != nil {
		t.Fatal(err)
	}
	id := statedb.Identity{
		FleetID: "test-fleet", CACert: []byte("ca"), CAKey: []byte("key"),
		ServerCert: []byte("cert"), ServerKey: []byte("skey"),
		AccountSalt: bytes32(), SaltVersion: 1, SettingsEpoch: "e",
		CreatedTime: weekReset,
	}
	if err := statedb.Create(context.Background(), dir, id, statedb.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	s, err := statedb.Open(context.Background(), dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func bytes32() []byte {
	out := make([]byte, 32)
	for i := range out {
		out[i] = 7
	}
	return out
}

func testTelemetry(t *testing.T) *telemetry.Telemetry {
	t.Helper()
	tel, err := telemetry.New(telemetry.Config{Service: telemetry.Hub, Version: "test", InstanceID: "test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tel.Shutdown(context.Background()) })
	return tel
}

// seedWeekReading stores one noon reading of the weekly bucket: used runs
// 10 to 75 through the week, then a reset to 5 after the Monday reset.
func seedWeekReading(t *testing.T, s *store.Store, at time.Time, used float64, resets time.Time) {
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
	buckets := append([]byte("["), append(raw, byte(']'))...)
	if _, err := statedb.InsertUsageReading(context.Background(), s, statedb.UsageSeed{
		AccountID: "acct-1", ReadTime: at, ReceivedTime: at.Add(time.Minute),
		AgentReadTime: at, Source: "PROBE", Parse: "OK",
		BucketsJSON: buckets,
	}); err != nil {
		t.Fatal(err)
	}
}

func seedWeek(t *testing.T, s *store.Store) {
	t.Helper()
	for i, used := range []float64{10, 20, 30, 45, 55, 65, 75, 5} {
		resets := weekReset
		if i == 7 {
			resets = weekReset.Add(7 * 24 * time.Hour)
		}
		seedWeekReading(t, s, time.Date(2026, time.October, 6+i, 12, 0, 0, 0, time.UTC), used, resets)
	}
}

func storedWindows(t *testing.T, s *store.Store) []*hubv1.QuotaWindow {
	t.Helper()
	rows, err := statedb.ListInsightQuotaWindows(context.Background(), s, "",
		weekReset.Add(-24*time.Hour), weekNow.Add(30*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	var out []*hubv1.QuotaWindow
	for _, r := range rows {
		var w hubv1.QuotaWindow
		if err := protojson.Unmarshal(r.WindowJSON, &w); err != nil {
			t.Fatalf("stored window is not a QuotaWindow: %v", err)
		}
		out = append(out, &w)
	}
	return out
}

// TestRollup_HandComputedWeek is the #41 acceptance line "rollup equals a
// hand-computed week": the stored rows equal the numbers worked out by
// hand from the readings and the 10-point default reserve. No account
// row exists, so the reserve default applies; no pace or spend rows
// exist yet, so behind, idle_behind and fleet_used read 0.
func TestRollup_HandComputedWeek(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	seedWeek(t, s)
	r, err := rollup.New(rollup.Options{Telemetry: testTelemetry(t)})
	if err != nil {
		t.Fatal(err)
	}
	res, err := r.Rollup(context.Background(), s, weekNow)
	if err != nil {
		t.Fatalf("Rollup: %v", err)
	}
	if res.Windows != 2 {
		t.Fatalf("windows = %d, want 2 (one complete week plus the trailing one)", res.Windows)
	}
	wins := storedWindows(t, s)
	if len(wins) != 2 {
		t.Fatalf("stored windows = %d, want 2", len(wins))
	}
	// Window 1: [Oct 6, Oct 13), complete. Last good reading in the
	// final 24 h is Oct 12 noon at 75; usable is 100-10=90, so
	// usable_used is 75/90 = 83.33 -> 83 and waste is 90-75 = 15.
	closed := wins[0]
	if !closed.GetStartTime().AsTime().Equal(weekReset) {
		t.Errorf("window 1 start = %v, want %v", closed.GetStartTime().AsTime(), weekReset)
	}
	if !closed.GetEndTime().AsTime().Equal(weekReset.Add(7 * 24 * time.Hour)) {
		t.Errorf("window 1 end = %v, want Oct 13", closed.GetEndTime().AsTime())
	}
	if !closed.GetComplete() {
		t.Error("window 1 complete = false, want true")
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
	if got := closed.GetReadings(); got != 7 {
		t.Errorf("readings = %d, want 7", got)
	}
	if got := closed.GetReservePercent(); got != 10 {
		t.Errorf("reserve = %d, want the 10-point default", got)
	}
	if got := closed.GetBehind().AsDuration(); got != 0 {
		t.Errorf("behind = %v, want 0 (no pace rows stored yet)", got)
	}
	if got := closed.GetFleetUsed().GetValue(); got != 0 {
		t.Errorf("fleet_used = %d, want 0 (no spend rows stored yet)", got)
	}
	// Window 2: [Oct 13, Oct 20), still open. No reading falls in its
	// final 24 h yet, so every percent is unknown, never 0.
	open := wins[1]
	if open.GetComplete() {
		t.Error("window 2 complete = true, want false (its reset is Oct 20)")
	}
	if _, ok := open.GetUsedAtEnd().GetKind().(*typev1.Percent_Unknown); !ok {
		t.Errorf("window 2 used_at_end kind = %T, want unknown", open.GetUsedAtEnd().GetKind())
	}
	if got := open.GetReadings(); got != 1 {
		t.Errorf("window 2 readings = %d, want 1", got)
	}
	computed, start, err := statedb.InsightRollupMeta(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	if !computed.Equal(weekNow) {
		t.Errorf("computed = %v, want %v", computed, weekNow)
	}
	if !start.Equal(weekReset) {
		t.Errorf("data_start = %v, want %v", start, weekReset)
	}
}

func TestRollup_RerunRewritesNothingNew(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	seedWeek(t, s)
	r, err := rollup.New(rollup.Options{Telemetry: testTelemetry(t)})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	first, err := r.Rollup(ctx, s, weekNow)
	if err != nil {
		t.Fatal(err)
	}
	second, err := r.Rollup(ctx, s, weekNow.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if first.Windows != 2 || second.Windows != 2 {
		t.Errorf("windows = %d then %d, want 2 then 2", first.Windows, second.Windows)
	}
	if got := len(storedWindows(t, s)); got != 2 {
		t.Errorf("stored windows after a rerun = %d, want 2 (upsert by key)", got)
	}
}

func TestRollup_ReserveComesFromTheAccountSpec(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	seedWeek(t, s)
	// The account keeps 20 for interactive use: usable is 80, so 75
	// used is 75/80 = 93.75 -> 94 with 5 points of waste.
	if _, err := statedb.CreateOwnerAccount(context.Background(), s, statedb.CreateAccount{
		ConfigWrite: statedb.ConfigWrite{
			Principal: "local:alice", RequestID: "req-1",
			RequestHash: [32]byte{1}, Actor: statedb.Actor{ID: "local:alice", Kind: "LOCAL_USER", Name: "alice"},
			Now: weekReset,
		},
		ID: "acct-1", Harness: "claude", Provider: "anthropic",
		DisplayName: "work", Origin: statedb.AccountOriginOwner,
		Spec: []byte(`{"displayName":"work","reservePercent":20}`),
	}, nil); err != nil {
		t.Fatalf("CreateOwnerAccount: %v", err)
	}
	r, err := rollup.New(rollup.Options{Telemetry: testTelemetry(t)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Rollup(context.Background(), s, weekNow); err != nil {
		t.Fatalf("Rollup: %v", err)
	}
	wins := storedWindows(t, s)
	if len(wins) != 2 {
		t.Fatalf("stored windows = %d, want 2", len(wins))
	}
	if got := wins[0].GetUsableUsed().GetValue(); got != 94 {
		t.Errorf("usable_used = %d, want 94 (75 of 80 usable)", got)
	}
	if got := wins[0].GetUnused().GetValue(); got != 5 {
		t.Errorf("unused = %d, want 5", got)
	}
	if got := wins[0].GetReservePercent(); got != 20 {
		t.Errorf("reserve = %d, want 20 from the spec", got)
	}
}

// TestRollup_ReserveHistoryAppliesPerWindow: the usable line uses the
// reserve in force when the window ended (05 section 3). An account
// created with reserve 20 and raised to 40 after the first window
// closed rolls the closed window at 20 (75 used of 80 usable: 94 with
// 5 points of waste) and the open window at 40.
func TestRollup_ReserveHistoryAppliesPerWindow(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	seedWeek(t, s)
	ctx := context.Background()
	if _, err := statedb.CreateOwnerAccount(ctx, s, statedb.CreateAccount{
		ConfigWrite: statedb.ConfigWrite{
			Principal: "local:alice", RequestID: "req-1",
			RequestHash: [32]byte{1}, Actor: statedb.Actor{ID: "local:alice", Kind: "LOCAL_USER", Name: "alice"},
			Now: weekReset,
		},
		ID: "acct-1", Harness: "claude", Provider: "anthropic",
		DisplayName: "work", Origin: statedb.AccountOriginOwner,
		Spec: []byte(`{"displayName":"work","reservePercent":20}`),
	}, nil); err != nil {
		t.Fatalf("CreateOwnerAccount: %v", err)
	}
	raised := weekReset.Add(7*24*time.Hour + 12*time.Hour)
	merge := func(current []byte) ([]byte, string, error) {
		return []byte(`{"displayName":"work","reservePercent":40}`), "work", nil
	}
	if _, err := statedb.UpdateOwnerAccount(ctx, s, statedb.UpdateAccountOp{
		ConfigWrite: statedb.ConfigWrite{
			Principal: "local:alice", RequestID: "req-2",
			RequestHash: [32]byte{2}, Actor: statedb.Actor{ID: "local:alice", Kind: "LOCAL_USER", Name: "alice"},
			Now: raised,
		},
		ID: "acct-1", Merge: merge,
	}, nil); err != nil {
		t.Fatalf("UpdateOwnerAccount: %v", err)
	}
	r, err := rollup.New(rollup.Options{Telemetry: testTelemetry(t)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Rollup(ctx, s, weekNow); err != nil {
		t.Fatalf("Rollup: %v", err)
	}
	wins := storedWindows(t, s)
	if len(wins) != 2 {
		t.Fatalf("stored windows = %d, want 2", len(wins))
	}
	if got := wins[0].GetReservePercent(); got != 20 {
		t.Errorf("closed window reserve = %d, want 20 in force when it ended", got)
	}
	if got := wins[0].GetUsableUsed().GetValue(); got != 94 {
		t.Errorf("closed window usable_used = %d, want 94 (75 of 80 usable)", got)
	}
	if got := wins[0].GetUnused().GetValue(); got != 5 {
		t.Errorf("closed window unused = %d, want 5", got)
	}
	if got := wins[1].GetReservePercent(); got != 40 {
		t.Errorf("open window reserve = %d, want the raised 40", got)
	}
}

// TestRollup_BoundedInputsMatchFullInputs: the hourly pass reads from
// the bounded scan, so a bound before the history must group the same
// inputs as the full scan, and a bound past the history must read
// nothing.
func TestRollup_BoundedInputsMatchFullInputs(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	seedWeek(t, s)
	ctx := context.Background()

	full, err := rollup.Inputs(ctx, s, weekNow)
	if err != nil {
		t.Fatalf("Inputs: %v", err)
	}
	bounded, err := rollup.InputsSince(ctx, s, weekReset.Add(-rollup.ReadMargin), weekNow)
	if err != nil {
		t.Fatalf("InputsSince: %v", err)
	}
	if len(full) != 1 || len(bounded) != 1 {
		t.Fatalf("inputs = %d full, %d bounded, want 1 and 1", len(full), len(bounded))
	}
	if len(full[0].Buckets) != len(bounded[0].Buckets) ||
		len(full[0].Buckets[0].Readings) != len(bounded[0].Buckets[0].Readings) {
		t.Errorf("bounded readings = %v, want the full %v",
			len(bounded[0].Buckets[0].Readings), len(full[0].Buckets[0].Readings))
	}

	empty, err := rollup.InputsSince(ctx, s, weekNow, weekNow)
	if err != nil {
		t.Fatalf("InputsSince past history: %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("inputs past history = %d accounts, want 0", len(empty))
	}
}

func TestRollup_EmptyStoreWritesNothing(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	r, err := rollup.New(rollup.Options{Telemetry: testTelemetry(t)})
	if err != nil {
		t.Fatal(err)
	}
	res, err := r.Rollup(context.Background(), s, weekNow)
	if err != nil {
		t.Fatalf("Rollup: %v", err)
	}
	if res.Windows != 0 {
		t.Errorf("windows = %d, want 0 with no readings", res.Windows)
	}
	if got := len(storedWindows(t, s)); got != 0 {
		t.Errorf("stored windows = %d, want 0", got)
	}
}
