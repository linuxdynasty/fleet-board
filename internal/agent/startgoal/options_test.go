package startgoal

import (
	"context"
	"testing"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/exec"
)

func readyReuse(t *testing.T) (Deps, *fakeTyper, *fakeScreens, string, *fakePanes) {
	t.Helper()
	panes := reusePanes("")
	d, typer, screens, _, _, tmp := testDeps(t, panes)
	checkout := tmp + "/checkout"
	for k, ps := range panes.states {
		ps.PaneDir, ps.CheckoutTop = checkout, checkout
		panes.states[k] = ps
	}
	panes.repos["github.com/acme/repo"] = checkout
	panes.push["github.com/acme/repo"] = true
	d = withWindows(d, testPane())
	screens.screens = []string{readyScreen()}
	return d, typer, screens, checkout, panes
}

func TestModelEffortClear(t *testing.T) {
	t.Parallel()
	d, typer, _, checkout, panes := readyReuse(t)
	spec := testSpec(checkout)
	spec.Model, spec.Effort, spec.FreshContext = "opus", "high", true
	ps := panes.states[paneID(testPane())]
	ps.FleetSession = true
	panes.states[paneID(testPane())] = ps
	r, err := New(d)
	if err != nil {
		t.Fatal(err)
	}
	res := r.Run(context.Background(), startCmd(spec, testPane()), nil)
	if res.GetStatus() != typev1.Status_STATUS_OK {
		t.Fatalf("Run = %v %q", res.GetStatus(), res.GetMessage())
	}
	var got []string
	for _, c := range typer.calls {
		got = append(got, c.command)
	}
	want := []string{"/model", "/effort", "/clear", "/goal migrate the parser (consent: push)"}
	if len(got) != len(want) {
		t.Fatalf("typed = %v; want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("typed = %v; want %v", got, want)
		}
	}
	if typer.calls[0].class != typev1.CommandClass_COMMAND_CLASS_SESSION ||
		typer.calls[0].args != "opus" {
		t.Fatalf("model call = %+v", typer.calls[0])
	}
	if res.GetStartGoal().GetModel() != "opus" || res.GetStartGoal().GetEffort() != "high" {
		t.Fatalf("effective = %+v", res.GetStartGoal())
	}
}

func TestClearNeedsMark(t *testing.T) {
	t.Parallel()
	d, typer, _, checkout, _ := readyReuse(t)
	spec := testSpec(checkout)
	spec.FreshContext = true
	r, err := New(d)
	if err != nil {
		t.Fatal(err)
	}
	res := r.Run(context.Background(), startCmd(spec, testPane()), nil)
	if res.GetStatus() != typev1.Status_STATUS_FAILED ||
		res.GetReason() != typev1.ReasonCode_REASON_CODE_FRESH_CONTEXT_NOT_ALLOWED {
		t.Fatalf("Run = %v %v %q; want FRESH_CONTEXT_NOT_ALLOWED",
			res.GetStatus(), res.GetReason(), res.GetMessage())
	}
	if len(typer.calls) != 0 {
		t.Fatalf("typed %+v after a refusal", typer.calls)
	}
}

func TestEffortUnsupported(t *testing.T) {
	t.Parallel()
	d, typer, screens, checkout, _ := readyReuse(t)
	spec := testSpec(checkout)
	spec.Harness = &typev1.Harness{Name: "cursor"}
	spec.Effort = "high"
	screens.screens = []string{"\x1b[2m→ Plan, search, build anything\x1b[0m"}
	r, err := New(d)
	if err != nil {
		t.Fatal(err)
	}
	res := r.Run(context.Background(), startCmd(spec, nil), nil)
	if res.GetStatus() != typev1.Status_STATUS_FAILED ||
		res.GetReason() != typev1.ReasonCode_REASON_CODE_EFFORT_NOT_SUPPORTED {
		t.Fatalf("Run = %v %v %q; want EFFORT_NOT_SUPPORTED",
			res.GetStatus(), res.GetReason(), res.GetMessage())
	}
	if len(typer.calls) != 0 {
		t.Fatalf("typed %+v after a refusal", typer.calls)
	}
}

func TestGoalFileNotSeen(t *testing.T) {
	t.Parallel()
	panes := reusePanes("")
	d, typer, screens, _, _, tmp := testDeps(t, panes)
	checkout := tmp + "/checkout"
	panes.repos["github.com/acme/repo"] = checkout
	panes.push["github.com/acme/repo"] = true
	d = withWindows(d, testPane())
	screens.screens = []string{readyScreen()}
	r, err := New(d)
	if err != nil {
		t.Fatal(err)
	}
	spec := testSpec(checkout)
	spec.WorktreeMode = agentv1.WorktreeMode_WORKTREE_MODE_REPO_DRIVER
	res := r.Run(context.Background(), quickCmd(spec, nil), nil)
	if res.GetStatus() != typev1.Status_STATUS_FAILED ||
		res.GetReason() != typev1.ReasonCode_REASON_CODE_GOAL_FILE_NOT_SEEN {
		t.Fatalf("Run = %v %v %q; want GOAL_FILE_NOT_SEEN",
			res.GetStatus(), res.GetReason(), res.GetMessage())
	}
	if res.GetRetryable() {
		t.Fatal("GOAL_FILE_NOT_SEEN must not be retryable: the type already happened")
	}
	if len(typer.calls) != 1 {
		t.Fatalf("typed = %+v; want the one goal type before the failure", typer.calls)
	}
}

func TestShapeRefusals(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		mutate func(*agentv1.GoalSpec)
	}{
		{name: "opencode has no native goal", mutate: func(s *agentv1.GoalSpec) {
			s.Harness = &typev1.Harness{Name: "opencode"}
		}},
		{name: "scratch carries no consent", mutate: func(s *agentv1.GoalSpec) {
			s.WorktreeMode = agentv1.WorktreeMode_WORKTREE_MODE_SCRATCH
		}},
		{name: "bad goal id", mutate: func(s *agentv1.GoalSpec) {
			s.GoalId = &typev1.GoalId{Value: "GOAL!!"}
		}},
		{name: "re-arm without pane", mutate: func(s *agentv1.GoalSpec) {}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d, typer, _, checkout, _ := readyReuse(t)
			spec := testSpec(checkout)
			tc.mutate(spec)
			r, err := New(d)
			if err != nil {
				t.Fatal(err)
			}
			var reuse *typev1.PaneKey
			var cmd *agentv1.StartGoal
			if tc.name == "re-arm without pane" {
				cmd = quickCmd(spec, nil)
				cmd.RearmReason = "again"
			} else {
				cmd = quickCmd(spec, reuse)
			}
			res := r.Run(context.Background(), cmd, nil)
			if res.GetStatus() != typev1.Status_STATUS_FAILED {
				t.Fatalf("Run = %v %q; want FAILED", res.GetStatus(), res.GetMessage())
			}
			if len(typer.calls) != 0 {
				t.Fatalf("typed %+v after a refusal", typer.calls)
			}
		})
	}
}

func TestHandlerContract(t *testing.T) {
	t.Parallel()
	h, err := NewHandler(Deps{
		Panes: &fakePanes{}, Git: &fakeGit{}, Trees: &fakeTrees{},
		Windows: &fakeWindows{}, Registry: nil, Typer: &fakeTyper{},
		Screens: &fakeScreens{}, Capture: &fakeCapture{},
		MaxGoals: func() int { return 1 }, DiskFree: func() (int64, bool) { return 1, true },
	})
	if err == nil {
		t.Fatal("NewHandler without a registry must fail")
	}
	_ = h
	d, _, _, _, _, _ := testDeps(t, &fakePanes{})
	d = withWindows(d, testPane())
	h, err = NewHandler(d)
	if err != nil {
		t.Fatal(err)
	}
	if h.Kind() != "StartGoal" {
		t.Fatalf("Kind = %q", h.Kind())
	}
	caps := h.Capabilities()
	if len(caps) != 1 || caps[0].GetName() != "goal.start" {
		t.Fatalf("Capabilities = %+v", caps)
	}
	if h.Class(nil) != exec.PerStep {
		t.Fatal("StartGoal must resume per step (02h restart classes)")
	}
	bad := &agentv1.Command{Kind: &agentv1.Command_EnsureSession{}}
	req := exec.Request{Command: bad}
	if res := h.Run(context.Background(), req); res.GetReason() != typev1.ReasonCode_REASON_CODE_INTERNAL {
		t.Fatalf("wrong kind = %v", res)
	}
	if res := h.Resume(context.Background(), req); res.GetReason() != typev1.ReasonCode_REASON_CODE_INTERNAL {
		t.Fatalf("wrong kind resume = %v", res)
	}
}
