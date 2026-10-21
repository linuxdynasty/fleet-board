package main_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// insightsCmd runs `fleetd insights` and returns the combined output
// and the exit code.
func insightsCmd(t *testing.T, config string, args ...string) (string, int) {
	t.Helper()
	cmd := fleetd(t, append([]string{"insights", "--config", config}, args...)...)
	out, err := cmd.CombinedOutput()
	return string(out), exitCode(err)
}

// seedInsightsWeek stores one weekly-bucket reading per noon, 10 to 75
// through the week and 5 after the Monday reset.
func seedInsightsWeek(t *testing.T, state string) {
	t.Helper()
	s, err := statedb.Open(context.Background(), state, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	for i, used := range []float64{10, 20, 30, 45, 55, 65, 75, 5} {
		resets := time.Date(2026, time.October, 6, 0, 0, 0, 0, time.UTC)
		if i == 7 {
			resets = resets.Add(7 * 24 * time.Hour)
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
		if _, err := statedb.InsertUsageReading(context.Background(), s, statedb.UsageSeed{
			AccountID: "acct-1", ReadTime: at, ReceivedTime: at.Add(time.Minute),
			AgentReadTime: at, Source: "PROBE", Parse: "OK",
			BucketsJSON: append(append([]byte("["), raw...), byte(']')),
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func countInsightWindows(t *testing.T, state string) int {
	t.Helper()
	db, err := sql.Open("sqlite", statedb.Path(state))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var n int
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM insight_quota_window`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// `fleetd insights rebuild --from` recomputes the efficiency rollups
// from the stored readings and prints what it wrote.
func TestInsightsRebuildRecomputesTheWeek(t *testing.T) {
	t.Parallel()
	config, state := setup(t)
	if out, err := fleetd(t, "init", "--config", config).CombinedOutput(); err != nil {
		t.Fatalf("fleetd init: %v\n%s", err, out)
	}
	seedInsightsWeek(t, state)
	combined, code := insightsCmd(t, config, "rebuild", "--from", "2026-10-06")
	if code != 0 {
		t.Fatalf("fleetd insights rebuild: exit %d\n%s", code, combined)
	}
	if !strings.Contains(combined, "rebuilt 2 windows for 1 account") {
		t.Errorf("stdout %q, want the rebuilt count", combined)
	}
	if got := countInsightWindows(t, state); got != 2 {
		t.Errorf("stored windows = %d, want 2", got)
	}
}

// The rebuild needs its --from day, in YYYY-MM-DD, and refuses a
// missing or unparseable one before touching the store.
func TestInsightsRebuildNeedsAFromDay(t *testing.T) {
	t.Parallel()
	config, _ := setup(t)
	if out, err := fleetd(t, "init", "--config", config).CombinedOutput(); err != nil {
		t.Fatalf("fleetd init: %v\n%s", err, out)
	}
	for _, args := range [][]string{{"rebuild"}, {"rebuild", "--from", "Oct 6"}} {
		combined, code := insightsCmd(t, config, args...)
		if code == 0 {
			t.Errorf("fleetd insights %v: exit 0, want non-zero", args)
		}
		if !strings.Contains(combined, "--from") {
			t.Errorf("fleetd insights %v output %q, want it to name --from", args, combined)
		}
	}
}
