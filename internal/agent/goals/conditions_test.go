package goals_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// loadHarnessScreen reads one committed screen fixture from an adapter's
// testdata, so the condition observer is tested on the same bytes the
// adapter's own parser test proves (01 E12).
func loadHarnessScreen(t *testing.T, harness, version, name string) string {
	t.Helper()
	//nolint:gosec // G304: a fixture under the repo's own testdata, named by the test, test code only
	raw, err := os.ReadFile(filepath.Join("..", "..", "harness", harness, "testdata", version, name))
	if err != nil {
		t.Fatalf("read %s %s/%s: %v", harness, version, name, err)
	}
	return string(raw)
}

// interruptCases covers one interrupt or stall screen per harness that
// reports conditions: Claude, Codex and Muse read GOAL_INTERRUPTED after
// the owner pressed Esc (so DEVELOPER_INPUT), Cursor reads its stall line
// as GOAL_PAUSED (the harness paused itself: HARNESS).
var interruptCases = []struct {
	name        string
	harness     string
	version     string
	interrupted string
	ready       string
	wantKind    typev1.HarnessConditionKind
	wantCause   typev1.GoalPauseCause
	wantText    string
}{
	{
		name: "claude interrupted", harness: "claude", version: "2.1.296",
		interrupted: "interrupted.txt", ready: "startup-ready.txt",
		wantKind:  typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_GOAL_INTERRUPTED,
		wantCause: typev1.GoalPauseCause_GOAL_PAUSE_CAUSE_DEVELOPER_INPUT,
		wantText:  "Interrupted",
	},
	{
		name: "codex goal paused", harness: "codex", version: "0.160.0",
		interrupted: "goal-paused.txt", ready: "ready.txt",
		wantKind:  typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_GOAL_INTERRUPTED,
		wantCause: typev1.GoalPauseCause_GOAL_PAUSE_CAUSE_DEVELOPER_INPUT,
		wantText:  "Goal paused",
	},
	{
		name: "muse goal paused by interrupt", harness: "muse", version: "1.4.4",
		interrupted: "goal-paused-by-interrupt.txt", ready: "startup-ready.txt",
		wantKind:  typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_GOAL_INTERRUPTED,
		wantCause: typev1.GoalPauseCause_GOAL_PAUSE_CAUSE_DEVELOPER_INPUT,
		wantText:  "goal paused by interrupt",
	},
	{
		name: "cursor goal stalled", harness: "cursor", version: "2026.10.01",
		interrupted: "goal-paused.txt", ready: "ready-followup-placeholder.txt",
		wantKind:  typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_GOAL_PAUSED,
		wantCause: typev1.GoalPauseCause_GOAL_PAUSE_CAUSE_HARNESS,
		wantText:  "Goal paused (12s)",
	},
	{
		name: "muse provider error retrying", harness: "muse", version: "1.4.7",
		interrupted: "provider-error-retrying.txt", ready: "startup-ready.txt",
		wantKind:  typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_PROVIDER_ERROR,
		wantCause: typev1.GoalPauseCause_GOAL_PAUSE_CAUSE_UNSPECIFIED,
		wantText:  "retrying",
	},
	{
		name: "muse rate limit retrying", harness: "muse", version: "1.4.7",
		interrupted: "rate-limit-retrying.txt", ready: "startup-ready.txt",
		wantKind:  typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_RATE_LIMITED,
		wantCause: typev1.GoalPauseCause_GOAL_PAUSE_CAUSE_UNSPECIFIED,
		wantText:  "retrying",
	},
	{
		name: "muse usage limit reached", harness: "muse", version: "1.4.7",
		interrupted: "usage-limit-reached.txt", ready: "startup-ready.txt",
		wantKind:  typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_USAGE_LIMIT,
		wantCause: typev1.GoalPauseCause_GOAL_PAUSE_CAUSE_UNSPECIFIED,
		wantText:  "Usage limit reached",
	},
}

// startGoalWithHarness attributes one observed goal on pane %1 running the
// named harness, and returns its goal id.
func startGoalWithHarness(t *testing.T, r *rig, harness string) string {
	t.Helper()
	r.procs.harness[100] = procHarness{name: harness, start: r.now.Add(-time.Hour)}
	r.writeGoal("Working\n", r.now.Add(-time.Minute))
	r.scan()
	got := r.goals()
	if len(got) != 1 {
		t.Fatalf("goals = %d, want 1", len(got))
	}
	return got[0].GetGoalId().GetValue()
}

func scanConditions(t *testing.T, r *rig) {
	t.Helper()
	if err := r.tr.ScanConditions(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func conditionsOf(snap *agentv1.Snapshot) []*agentv1.HarnessCondition {
	return snap.GetConditions()
}

func harnessConditions(evts []*agentv1.Event) []*agentv1.HarnessCondition {
	var out []*agentv1.HarnessCondition
	for _, ev := range evts {
		if c := ev.GetHarnessCondition(); c != nil {
			out = append(out, c)
		}
	}
	return out
}

// An interrupt screen on a goal pane emits one open condition with the
// registry goal id, and the snapshot lists it while it shows.
func TestScanConditionsEmitsInterrupt(t *testing.T) {
	t.Parallel()
	for _, tc := range interruptCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			goalID := startGoalWithHarness(t, r, tc.harness)
			r.captures.screens["%1"] = loadHarnessScreen(t, tc.harness, tc.version, tc.ready)
			scanConditions(t, r) // prime: the snapshot carries the level, no events
			if len(r.emitted) != 0 {
				t.Fatalf("first scan emitted %d events, want none", len(r.emitted))
			}
			snap := &agentv1.Snapshot{}
			r.tr.FillConditions(snap)
			if len(conditionsOf(snap)) != 0 {
				t.Fatalf("snapshot lists %d conditions, want none on a ready screen", len(conditionsOf(snap)))
			}
			r.captures.screens["%1"] = loadHarnessScreen(t, tc.harness, tc.version, tc.interrupted)
			scanConditions(t, r)
			got := harnessConditions(r.emitted)
			if len(got) != 1 {
				t.Fatalf("emitted %d conditions, want 1", len(got))
			}
			c := got[0]
			if c.GetCleared() {
				t.Fatalf("cleared = true, want the open pair first")
			}
			if c.GetKind() != tc.wantKind {
				t.Fatalf("kind = %v, want %v", c.GetKind(), tc.wantKind)
			}
			if c.GetPauseCause() != tc.wantCause {
				t.Fatalf("pause_cause = %v, want %v", c.GetPauseCause(), tc.wantCause)
			}
			if !strings.Contains(c.GetText(), tc.wantText) {
				t.Fatalf("text = %q, want it to hold %q", c.GetText(), tc.wantText)
			}
			if c.GetGoalId().GetValue() != goalID {
				t.Fatalf("goal_id = %q, want the registry goal %q", c.GetGoalId().GetValue(), goalID)
			}
			if c.GetPane().GetPaneId() != "%1" {
				t.Fatalf("pane = %v, want %%1", c.GetPane())
			}
			if c.GetHarness().GetName() != tc.harness {
				t.Fatalf("harness = %q, want %q", c.GetHarness().GetName(), tc.harness)
			}
			if c.GetSinceTime() == nil {
				t.Fatalf("since_time is unset")
			}
			snap = &agentv1.Snapshot{}
			r.tr.FillConditions(snap)
			listed := conditionsOf(snap)
			if len(listed) != 1 || listed[0].GetKind() != tc.wantKind {
				t.Fatalf("snapshot lists %v, want the one open condition", listed)
			}
			if listed[0].GetGoalId().GetValue() != goalID {
				t.Fatalf("snapshot goal_id = %q, want %q", listed[0].GetGoalId().GetValue(), goalID)
			}
		})
	}
}

// When the screen returns to ready, the observer emits the cleared pair
// and the snapshot no longer lists the condition.
func TestScanConditionsClearsWhenScreenRecovers(t *testing.T) {
	t.Parallel()
	for _, tc := range interruptCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			goalID := startGoalWithHarness(t, r, tc.harness)
			r.captures.screens["%1"] = loadHarnessScreen(t, tc.harness, tc.version, tc.interrupted)
			scanConditions(t, r) // prime with the condition showing: silent
			snap := &agentv1.Snapshot{}
			r.tr.FillConditions(snap)
			if len(conditionsOf(snap)) != 1 {
				t.Fatalf("snapshot lists %d conditions, want the primed one", len(conditionsOf(snap)))
			}
			r.captures.screens["%1"] = loadHarnessScreen(t, tc.harness, tc.version, tc.ready)
			scanConditions(t, r)
			got := harnessConditions(r.emitted)
			if len(got) != 1 {
				t.Fatalf("emitted %d conditions, want the cleared pair", len(got))
			}
			if !got[0].GetCleared() {
				t.Fatalf("cleared = false, want true")
			}
			if got[0].GetKind() != tc.wantKind || got[0].GetGoalId().GetValue() != goalID {
				t.Fatalf("cleared %+v, want kind %v for goal %q", got[0], tc.wantKind, goalID)
			}
			snap = &agentv1.Snapshot{}
			r.tr.FillConditions(snap)
			if len(conditionsOf(snap)) != 0 {
				t.Fatalf("snapshot still lists %d conditions, want none", len(conditionsOf(snap)))
			}
			// A further quiet screen emits nothing more.
			scanConditions(t, r)
			if len(harnessConditions(r.emitted)) != 1 {
				t.Fatalf("emitted %d conditions, want no repeat of the cleared pair", len(harnessConditions(r.emitted)))
			}
		})
	}
}

// A rate-limit screen on a goal pane emits the condition with the retry
// detail the parser read (01 E12): retrying set, the attempt counter, the
// HTTP status and the reset, so the hub can hold the goal in LIMIT_WAIT
// (03h). The snapshot lists the same detail.
func TestScanConditionsCarriesRateLimitDetail(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	goalID := startGoalWithHarness(t, r, "muse")
	r.captures.screens["%1"] = loadHarnessScreen(t, "muse", "1.4.7", "startup-ready.txt")
	scanConditions(t, r) // prime quiet
	r.captures.screens["%1"] = loadHarnessScreen(t, "muse", "1.4.7", "rate-limit-retrying.txt")
	scanConditions(t, r)
	got := harnessConditions(r.emitted)
	if len(got) != 1 {
		t.Fatalf("emitted %d conditions, want 1", len(got))
	}
	c := got[0]
	if c.GetKind() != typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_RATE_LIMITED {
		t.Fatalf("kind = %v, want RATE_LIMITED", c.GetKind())
	}
	if !c.GetRetrying() {
		t.Errorf("retrying = false, want true: the harness retries on its own")
	}
	if c.GetAttempt() != 4 || c.GetMaxAttempts() != 10 {
		t.Errorf("attempt/max = %d/%d, want 4/10", c.GetAttempt(), c.GetMaxAttempts())
	}
	if c.GetHttpStatus() != 529 {
		t.Errorf("http_status = %d, want 529", c.GetHttpStatus())
	}
	if c.GetResetsTime() == nil {
		t.Fatalf("resets_time is unset, want the shown reset")
	}
	if rt := c.GetResetsTime().AsTime().In(time.Local); rt.Hour() != 21 || rt.Minute() != 29 {
		t.Errorf("resets_time = %v local, want 21:29", rt)
	}
	if c.GetGoalId().GetValue() != goalID {
		t.Errorf("goal_id = %q, want the registry goal %q", c.GetGoalId().GetValue(), goalID)
	}
	snap := &agentv1.Snapshot{}
	r.tr.FillConditions(snap)
	listed := conditionsOf(snap)
	if len(listed) != 1 {
		t.Fatalf("snapshot lists %d conditions, want 1", len(listed))
	}
	s := listed[0]
	if !s.GetRetrying() || s.GetAttempt() != 4 || s.GetMaxAttempts() != 10 ||
		s.GetHttpStatus() != 529 || s.GetResetsTime() == nil {
		t.Errorf("snapshot condition drops the retry detail: %+v", s)
	}
}

// A usage-limit screen on a goal pane emits the condition with the reset
// the parser read (01 E12): retrying unset, no attempt counter or status,
// so the hub can hold the goal in LIMIT_WAIT until the reset (03h). The
// snapshot lists the same detail.
func TestScanConditionsCarriesUsageLimitReset(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	goalID := startGoalWithHarness(t, r, "muse")
	r.captures.screens["%1"] = loadHarnessScreen(t, "muse", "1.4.7", "startup-ready.txt")
	scanConditions(t, r) // prime quiet
	r.captures.screens["%1"] = loadHarnessScreen(t, "muse", "1.4.7", "usage-limit-reached.txt")
	scanConditions(t, r)
	got := harnessConditions(r.emitted)
	if len(got) != 1 {
		t.Fatalf("emitted %d conditions, want 1", len(got))
	}
	c := got[0]
	if c.GetKind() != typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_USAGE_LIMIT {
		t.Fatalf("kind = %v, want USAGE_LIMIT", c.GetKind())
	}
	if c.GetRetrying() {
		t.Errorf("retrying = true, want false: a limit waits, it does not retry")
	}
	if c.GetAttempt() != 0 || c.GetMaxAttempts() != 0 {
		t.Errorf("attempt/max = %d/%d, want 0/0", c.GetAttempt(), c.GetMaxAttempts())
	}
	if c.GetHttpStatus() != 0 {
		t.Errorf("http_status = %d, want 0", c.GetHttpStatus())
	}
	if c.GetResetsTime() == nil {
		t.Fatalf("resets_time is unset, want the shown reset")
	}
	if rt := c.GetResetsTime().AsTime().In(time.Local); rt.Hour() != 21 || rt.Minute() != 29 {
		t.Errorf("resets_time = %v local, want 21:29", rt)
	}
	if c.GetGoalId().GetValue() != goalID {
		t.Errorf("goal_id = %q, want the registry goal %q", c.GetGoalId().GetValue(), goalID)
	}
	snap := &agentv1.Snapshot{}
	r.tr.FillConditions(snap)
	listed := conditionsOf(snap)
	if len(listed) != 1 {
		t.Fatalf("snapshot lists %d conditions, want 1", len(listed))
	}
	s := listed[0]
	if s.GetRetrying() || s.GetAttempt() != 0 || s.GetMaxAttempts() != 0 ||
		s.GetHttpStatus() != 0 || s.GetResetsTime() == nil {
		t.Errorf("snapshot condition drops the reset detail: %+v", s)
	}
}

// A failed capture keeps the last level: a read failure never looks like
// a recovery.
func TestScanConditionsKeepsLevelOnCaptureError(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	startGoalWithHarness(t, r, "claude")
	r.captures.screens["%1"] = loadHarnessScreen(t, "claude", "2.1.296", "interrupted.txt")
	scanConditions(t, r)
	r.captures.screens["%1"] = loadHarnessScreen(t, "claude", "2.1.296", "startup-ready.txt")
	scanConditions(t, r)
	r.captures.fail["%1"] = true
	scanConditions(t, r)
	got := harnessConditions(r.emitted)
	if len(got) != 1 || !got[0].GetCleared() {
		t.Fatalf("emitted %v, want only the cleared pair before the failure", got)
	}
	snap := &agentv1.Snapshot{}
	r.tr.FillConditions(snap)
	if len(conditionsOf(snap)) != 0 {
		t.Fatalf("snapshot lists %d conditions, want none", len(conditionsOf(snap)))
	}
	// The open level also survives a failure: prime open, fail, still listed.
	r2 := newRig(t)
	startGoalWithHarness(t, r2, "claude")
	r2.captures.screens["%1"] = loadHarnessScreen(t, "claude", "2.1.296", "interrupted.txt")
	scanConditions(t, r2)
	r2.captures.fail["%1"] = true
	delete(r2.captures.screens, "%1")
	scanConditions(t, r2)
	if len(harnessConditions(r2.emitted)) != 0 {
		t.Fatalf("emitted %d conditions across a failed capture, want none", len(harnessConditions(r2.emitted)))
	}
	snap = &agentv1.Snapshot{}
	r2.tr.FillConditions(snap)
	if len(conditionsOf(snap)) != 1 {
		t.Fatalf("snapshot lists %d conditions, want the open one kept", len(conditionsOf(snap)))
	}
}

// When the goal ends, its open condition is cleared: the hub must not
// keep a triage item for a goal that is gone.
func TestScanConditionsClearsWhenGoalEnds(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	startGoalWithHarness(t, r, "codex")
	r.captures.screens["%1"] = loadHarnessScreen(t, "codex", "0.160.0", "goal-paused.txt")
	scanConditions(t, r)
	r.writeGoal("DONE\n", r.now.Add(time.Second))
	r.scan()
	scanConditions(t, r)
	got := harnessConditions(r.emitted)
	if len(got) != 1 || !got[0].GetCleared() {
		t.Fatalf("emitted %v, want the cleared pair for the ended goal", got)
	}
	snap := &agentv1.Snapshot{}
	r.tr.FillConditions(snap)
	if len(conditionsOf(snap)) != 0 {
		t.Fatalf("snapshot lists %d conditions, want none", len(conditionsOf(snap)))
	}
}

// Panes with no harness condition screen and harnesses without one
// (OpenCode has no /goal) never report.
func TestScanConditionsIgnoresQuietScreens(t *testing.T) {
	t.Parallel()
	quiet := []struct {
		harness string
		version string
		screen  string
	}{
		{"claude", "2.1.296", "usage.txt"},
		{"claude", "2.1.296", "status.txt"},
		{"claude", "2.1.296", "trust-folder.txt"},
		{"codex", "0.160.0", "status.txt"},
		{"muse", "1.4.4", "usage.txt"},
		{"cursor", "2026.10.01", "usage.txt"},
		{"opencode", "1.19.0", "tui-ready.txt"},
	}
	for _, q := range quiet {
		t.Run(q.harness+"/"+q.screen, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			harness := q.harness
			if harness == "opencode" {
				harness = "opencode"
			}
			r.procs.harness[100] = procHarness{name: harness, start: r.now.Add(-time.Hour)}
			r.writeGoal("Working\n", r.now.Add(-time.Minute))
			r.scan()
			if len(r.goals()) != 1 {
				t.Fatalf("goals = %d, want 1", len(r.goals()))
			}
			r.captures.screens["%1"] = loadHarnessScreen(t, q.harness, q.version, q.screen)
			scanConditions(t, r)
			scanConditions(t, r)
			if len(harnessConditions(r.emitted)) != 0 {
				t.Fatalf("emitted %d conditions on a quiet screen, want none", len(harnessConditions(r.emitted)))
			}
			snap := &agentv1.Snapshot{}
			r.tr.FillConditions(snap)
			if len(conditionsOf(snap)) != 0 {
				t.Fatalf("snapshot lists %d conditions, want none", len(conditionsOf(snap)))
			}
		})
	}
}
