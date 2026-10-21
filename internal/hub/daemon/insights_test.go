package daemon_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/daemon"
	"github.com/linuxdynasty/fleet-board/internal/hub/lock"
	"github.com/linuxdynasty/fleet-board/internal/hub/rollup"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

var (
	rebuildBase = time.Date(2026, time.October, 6, 0, 0, 0, 0, time.UTC)
	rebuildNow  = time.Date(2026, time.October, 14, 4, 0, 0, 0, time.UTC)
)

// seedRebuildWeek stores one weekly-bucket reading per noon, 10 to 75
// through the week and 5 after the Monday reset, so the rebuild has a
// hand-computable week to roll up.
func seedRebuildWeek(t *testing.T, h *hub) {
	t.Helper()
	st, err := statedb.Open(context.Background(), h.state, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	for i, used := range []float64{10, 20, 30, 45, 55, 65, 75, 5} {
		resets := rebuildBase
		if i == 7 {
			resets = rebuildBase.Add(7 * 24 * time.Hour)
		}
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
		at := time.Date(2026, time.October, 6+i, 12, 0, 0, 0, time.UTC)
		if _, err := statedb.InsertUsageReading(context.Background(), st, statedb.UsageSeed{
			AccountID: "acct-1", ReadTime: at, ReceivedTime: at.Add(time.Minute),
			AgentReadTime: at, Source: "PROBE", Parse: "OK",
			BucketsJSON: append(append([]byte("["), raw...), byte(']')),
		}); err != nil {
			t.Fatal(err)
		}
	}
}

// rebuildOptions runs the rebuild under a fixed clock.
func rebuildOptions(h *hub) daemon.Options {
	opts := h.options()
	opts.Now = func() time.Time { return rebuildNow }
	return opts
}

// TestInsightsRebuildRollsUpTheWeek: `fleetd insights rebuild --from`
// recomputes the windows ending in range from the full reading history
// and stamps the rollup meta.
func TestInsightsRebuildRollsUpTheWeek(t *testing.T) {
	t.Parallel()
	h := newHub(t, "")
	h.init(t)
	seedRebuildWeek(t, h)
	res, err := daemon.InsightsRebuild(context.Background(), rebuildOptions(h),
		daemon.InsightsRebuildRequest{From: rebuildBase})
	if err != nil {
		t.Fatalf("InsightsRebuild: %v", err)
	}
	if res.Windows != 2 {
		t.Errorf("windows = %d, want the complete week plus the trailing one", res.Windows)
	}
	if res.Accounts != 1 {
		t.Errorf("accounts = %d, want 1", res.Accounts)
	}
	st, err := statedb.Open(context.Background(), h.state, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	rows, err := statedb.ListInsightQuotaWindows(context.Background(), st, "",
		rebuildBase.Add(-time.Hour), rebuildNow.Add(30*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("stored windows = %d, want 2", len(rows))
	}
	computed, start, err := statedb.InsightRollupMeta(context.Background(), st)
	if err != nil {
		t.Fatal(err)
	}
	if !computed.Equal(rebuildNow) {
		t.Errorf("computed = %v, want %v", computed, rebuildNow)
	}
	if !start.Equal(rebuildBase) {
		t.Errorf("data_start = %v, want %v", start, rebuildBase)
	}
}

// TestInsightsRollupJobRunsHourly: the fleetd run job carries the
// hourly schedule and one run rolls the seeded week up.
func TestInsightsRollupJobRunsHourly(t *testing.T) {
	t.Parallel()
	h := newHub(t, "")
	h.init(t)
	seedRebuildWeek(t, h)
	st, err := statedb.Open(context.Background(), h.state, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	roller, err := rollup.New(rollup.Options{Telemetry: h.tel})
	if err != nil {
		t.Fatal(err)
	}
	job := daemon.InsightsRollupJob(roller, st)
	if job.Name != "insights_rollup" || job.Every != time.Hour {
		t.Errorf("job = %q every %v, want insights_rollup every 1h", job.Name, job.Every)
	}
	if err := job.Run(context.Background(), rebuildNow); err != nil {
		t.Fatalf("job run: %v", err)
	}
	rows, err := statedb.ListInsightQuotaWindows(context.Background(), st, "",
		rebuildBase.Add(-time.Hour), rebuildNow.Add(30*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Errorf("stored windows = %d, want 2 after one hourly pass", len(rows))
	}
}

// TestInsightsRebuildKeepsOlderWindows: a rebuild from a later date
// leaves earlier stored windows untouched.
func TestInsightsRebuildKeepsOlderWindows(t *testing.T) {
	t.Parallel()
	h := newHub(t, "")
	h.init(t)
	seedRebuildWeek(t, h)
	ctx := context.Background()
	if _, err := daemon.InsightsRebuild(ctx, rebuildOptions(h),
		daemon.InsightsRebuildRequest{From: rebuildBase}); err != nil {
		t.Fatal(err)
	}
	if _, err := daemon.InsightsRebuild(ctx, rebuildOptions(h),
		daemon.InsightsRebuildRequest{From: rebuildBase.Add(7 * 24 * time.Hour)}); err != nil {
		t.Fatalf("second rebuild: %v", err)
	}
	st, err := statedb.Open(ctx, h.state, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	rows, err := statedb.ListInsightQuotaWindows(ctx, st, "",
		rebuildBase.Add(-time.Hour), rebuildNow.Add(30*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Errorf("stored windows = %d, want 2 (older rows kept, newer rewritten)", len(rows))
	}
}

// TestInsightsRebuildRefusesAFutureDate: --from past now is a usage
// error, not a store write.
func TestInsightsRebuildRefusesAFutureDate(t *testing.T) {
	t.Parallel()
	h := newHub(t, "")
	h.init(t)
	_, err := daemon.InsightsRebuild(context.Background(), rebuildOptions(h),
		daemon.InsightsRebuildRequest{From: rebuildNow.Add(time.Hour)})
	if err == nil {
		t.Fatal("InsightsRebuild with --from past now succeeded, want an error")
	}
}

// TestInsightsRebuildIsRefusedWhileTheLockIsHeld: the rebuild opens
// state.db for writing, so it takes fleetd.lock and is refused while
// fleetd run holds it.
func TestInsightsRebuildIsRefusedWhileTheLockIsHeld(t *testing.T) {
	t.Parallel()
	h := newHub(t, "")
	h.init(t)
	l, err := lock.Acquire(filepath.Join(h.state, daemon.LockName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Release() })
	before, err := os.ReadFile(statedb.Path(h.state))
	if err != nil {
		t.Fatal(err)
	}
	_, err = daemon.InsightsRebuild(context.Background(), rebuildOptions(h),
		daemon.InsightsRebuildRequest{From: rebuildBase})
	if got := reasonOf(t, err); got != daemon.ReasonLockHeld {
		t.Errorf("InsightsRebuild with the lock held = %v (%s), want lock_held", err, got)
	}
	after, err := os.ReadFile(statedb.Path(h.state))
	if err != nil || string(after) != string(before) {
		t.Errorf("state.db changed under a held lock (%v)", err)
	}
}
