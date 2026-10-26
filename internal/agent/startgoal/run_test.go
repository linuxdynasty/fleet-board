package startgoal

import (
	"context"
	"testing"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"google.golang.org/protobuf/types/known/durationpb"
)

type fakeWindows struct {
	win   *Window
	res   *agentv1.CommandResult
	calls int
}

func (f *fakeWindows) Start(_ context.Context, _ *agentv1.SessionTarget, _, _, _, _, _, _ string) (*Window, *agentv1.CommandResult) {
	f.calls++
	return f.win, f.res
}

func reusePanes(top string) *fakePanes {
	return &fakePanes{
		states: map[string]PaneState{
			"default\x00s1\x00%1": {
				Exists: true, PaneDir: top, CheckoutTop: top,
				Harness: "claude", Ready: true, InputEmpty: true,
				Session: &agentv1.TmuxSession{SessionId: &typev1.SessionId{Value: "sess1"}},
			},
		},
		repos: map[string]string{"github.com/acme/repo": top},
		push:  map[string]bool{"github.com/acme/repo": true},
	}
}

func withWindows(d Deps, pane *typev1.PaneKey) Deps {
	d.Windows = &fakeWindows{win: &Window{
		Pane:    pane,
		Session: &agentv1.TmuxSession{SessionId: &typev1.SessionId{Value: "sess9"}},
	}}
	return d
}

func startCmd(spec *agentv1.GoalSpec, reuse *typev1.PaneKey) *agentv1.StartGoal {
	return &agentv1.StartGoal{
		Spec: spec, ReusePane: reuse,
		ReadyTimeout: durationpb.New(60000000000), GoalFileTimeout: durationpb.New(60000000000),
	}
}

func TestReuseHappyPath(t *testing.T) {
	t.Parallel()
	panes := reusePanes("")
	top := ""
	_ = top
	d, typer, screens, cap, reg, tmp := testDeps(t, panes)
	_ = tmp
	// Fix the checkout top now that the temp dir exists.
	checkout := tmp + "/checkout"
	for k, ps := range panes.states {
		ps.PaneDir, ps.CheckoutTop = checkout, checkout
		panes.states[k] = ps
	}
	panes.repos["github.com/acme/repo"] = checkout
	d = withWindows(d, testPane())
	screens.screens = []string{readyScreen()}
	r, err := New(d)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	rep := func(step string, _ int, failed bool, _ typev1.ReasonCode, _ string) {
		if !failed {
			got = append(got, step)
		}
	}
	spec := testSpec(checkout)
	res := r.Run(context.Background(), startCmd(spec, testPane()), rep)
	if res.GetStatus() != typev1.Status_STATUS_OK {
		t.Fatalf("Run = %v %q", res.GetStatus(), res.GetMessage())
	}
	wantSteps := []string{
		"preflight", "pick_session", "prepare_branch", "register_goal",
		"start_log_capture", "wait_ready", "write_goal_file", "type_goal", "confirm_goal_file",
	}
	if len(got) != len(wantSteps) {
		t.Fatalf("steps = %v; want %v", got, wantSteps)
	}
	for i := range wantSteps {
		if got[i] != wantSteps[i] {
			t.Fatalf("steps = %v; want %v", got, wantSteps)
		}
	}
	if len(typer.calls) != 1 || typer.calls[0].command != "/goal migrate the parser (consent: push)" {
		t.Fatalf("typed = %+v", typer.calls)
	}
	if len(cap.started) != 1 || cap.started[0] != "goal1" {
		t.Fatalf("capture = %v", cap.started)
	}
	entry, ok := reg.ByPane(testPane())
	if !ok || entry.GetWorktreePath() != checkout {
		t.Fatalf("registry = %+v, %v", entry, ok)
	}
	sr := res.GetStartGoal()
	if sr == nil || sr.GetTypedGoalLine() != "/goal migrate the parser (consent: push)" {
		t.Fatalf("result = %+v", res)
	}
	if sr.GetReusedSession() != true || sr.GetWorktreePath() != checkout {
		t.Fatalf("result reuse = %+v", sr)
	}
}

func TestNewWindowHappyPath(t *testing.T) {
	t.Parallel()
	panes := &fakePanes{
		repos: map[string]string{},
		push:  map[string]bool{},
	}
	d, typer, screens, _, reg, tmp := testDeps(t, panes)
	checkout := tmp + "/checkout"
	panes.repos["github.com/acme/repo"] = checkout
	panes.push["github.com/acme/repo"] = true
	trees, ok := d.Trees.(*fakeTrees)
	if !ok {
		t.Fatal("Trees is not the fake")
	}
	trees.wtPath = tmp + "/wt-goal1"
	pane := &typev1.PaneKey{Server: "default", ServerStartId: "s7", PaneId: "%3"}
	d = withWindows(d, pane)
	screens.screens = []string{readyScreen()}
	r, err := New(d)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	rep := func(step string, _ int, failed bool, _ typev1.ReasonCode, _ string) {
		if !failed {
			got = append(got, step)
		}
	}
	spec := testSpec(checkout)
	res := r.Run(context.Background(), startCmd(spec, nil), rep)
	if res.GetStatus() != typev1.Status_STATUS_OK {
		t.Fatalf("Run = %v %q", res.GetStatus(), res.GetMessage())
	}
	for _, want := range []string{"ensure_worktree", "ensure_window", "register_goal"} {
		found := false
		for _, s := range got {
			if s == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("steps = %v; want %s", got, want)
		}
	}
	for _, skip := range []string{"pick_session", "prepare_branch"} {
		for _, s := range got {
			if s == skip {
				t.Fatalf("steps = %v; %s must not run", got, skip)
			}
		}
	}
	if len(typer.calls) != 1 {
		t.Fatalf("typed = %+v", typer.calls)
	}
	entry, ok := reg.ByPane(pane)
	if !ok || entry.GetWorktreePath() != tmp+"/wt-goal1" {
		t.Fatalf("registry = %+v, %v", entry, ok)
	}
}

func TestPickSessionRefusals(t *testing.T) {
	t.Parallel()
	checkout := "/repo/top"
	other := "/repo/other"
	base := PaneState{
		Exists: true, PaneDir: checkout, CheckoutTop: checkout,
		Harness: "claude", Ready: true, InputEmpty: true,
	}
	cases := []struct {
		name   string
		mutate func(*PaneState)
		reason typev1.ReasonCode
	}{
		{
			name: "gone pane", mutate: func(ps *PaneState) { ps.Exists = false },
			reason: typev1.ReasonCode_REASON_CODE_SESSION_NOT_AVAILABLE,
		},
		{
			name: "bound to another goal", mutate: func(ps *PaneState) { ps.BoundGoal = "goal9" },
			reason: typev1.ReasonCode_REASON_CODE_SESSION_NOT_AVAILABLE,
		},
		{
			name: "another harness", mutate: func(ps *PaneState) { ps.Harness = "codex" },
			reason: typev1.ReasonCode_REASON_CODE_SESSION_NOT_AVAILABLE,
		},
		{
			name: "checkout held by another goal", mutate: func(ps *PaneState) { ps.CheckoutHeld = true },
			reason: typev1.ReasonCode_REASON_CODE_SESSION_NOT_AVAILABLE,
		},
		{
			name: "dirty checkout", mutate: func(ps *PaneState) { ps.Dirty = true },
			reason: typev1.ReasonCode_REASON_CODE_CHECKOUT_DIRTY,
		},
		{
			name: "busy input", mutate: func(ps *PaneState) { ps.InputDraft = true; ps.InputEmpty = false },
			reason: typev1.ReasonCode_REASON_CODE_PANE_BUSY,
		},
		{
			name: "not ready", mutate: func(ps *PaneState) { ps.Ready = false },
			reason: typev1.ReasonCode_REASON_CODE_PANE_BUSY,
		},
		{
			name: "subfolder mismatch", mutate: func(ps *PaneState) { ps.PaneDir = checkout + "/sub" },
			reason: typev1.ReasonCode_REASON_CODE_SESSION_WORKDIR_MISMATCH,
		},
		{
			name: "other checkout", mutate: func(ps *PaneState) { ps.PaneDir, ps.CheckoutTop = other, other },
			reason: typev1.ReasonCode_REASON_CODE_SESSION_WORKDIR_MISMATCH,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ps := base
			tc.mutate(&ps)
			panes := &fakePanes{
				states: map[string]PaneState{paneID(testPane()): ps},
				repos:  map[string]string{"github.com/acme/repo": checkout},
				push:   map[string]bool{"github.com/acme/repo": true},
			}
			d, typer, _, _, _, _ := testDeps(t, panes)
			_ = d
			r, err := New(withWindows(d, testPane()))
			if err != nil {
				t.Fatal(err)
			}
			res := r.Run(context.Background(), startCmd(testSpec(checkout), testPane()), nil)
			if res.GetStatus() != typev1.Status_STATUS_FAILED || res.GetReason() != tc.reason {
				t.Fatalf("Run = %v %v %q; want FAILED %v",
					res.GetStatus(), res.GetReason(), res.GetMessage(), tc.reason)
			}
			if len(typer.calls) != 0 {
				t.Fatalf("typed %+v after a refusal", typer.calls)
			}
		})
	}
}
