package startgoal

import (
	"context"
	"testing"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"google.golang.org/protobuf/types/known/durationpb"
)

func quickCmd(spec *agentv1.GoalSpec, reuse *typev1.PaneKey) *agentv1.StartGoal {
	return &agentv1.StartGoal{
		Spec: spec, ReusePane: reuse,
		ReadyTimeout: durationpb.New(40000000), GoalFileTimeout: durationpb.New(40000000),
	}
}

func TestPreflightFailures(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		setup  func(d *Deps, panes *fakePanes)
		reason typev1.ReasonCode
	}{
		{name: "goal cap reached", setup: func(d *Deps, _ *fakePanes) {
			d.MaxGoals = func() int { return 0 }
		}, reason: typev1.ReasonCode_REASON_CODE_AGENT_GOAL_CAP_REACHED},
		{name: "disk low", setup: func(d *Deps, _ *fakePanes) {
			d.DiskFree = func() (int64, bool) { return 1024, true }
		}, reason: typev1.ReasonCode_REASON_CODE_DISK_LOW},
		{name: "disk unreadable", setup: func(d *Deps, _ *fakePanes) {
			d.DiskFree = func() (int64, bool) { return 0, false }
		}, reason: typev1.ReasonCode_REASON_CODE_DISK_LOW},
		{name: "no checkout", setup: func(d *Deps, panes *fakePanes) {
			panes.repos = map[string]string{}
		}, reason: typev1.ReasonCode_REASON_CODE_CHECKOUT_MISSING},
		{name: "push access missing", setup: func(d *Deps, panes *fakePanes) {
			panes.push = map[string]bool{"github.com/acme/repo": false}
		}, reason: typev1.ReasonCode_REASON_CODE_PUSH_ACCESS_MISSING},
		{name: "reuse pane trusts nothing", setup: func(d *Deps, panes *fakePanes) {
			ps := panes.states[paneID(testPane())]
			ps.TrustBox = true
			panes.states[paneID(testPane())] = ps
		}, reason: typev1.ReasonCode_REASON_CODE_FOLDER_UNTRUSTED},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			top := ""
			panes := reusePanes(top)
			d, typer, _, _, _, tmp := testDeps(t, panes)
			checkout := tmp + "/checkout"
			for k, ps := range panes.states {
				ps.PaneDir, ps.CheckoutTop = checkout, checkout
				panes.states[k] = ps
			}
			panes.repos["github.com/acme/repo"] = checkout
			if _, ok := panes.push["github.com/acme/repo"]; !ok {
				panes.push["github.com/acme/repo"] = true
			}
			// The no-checkout case clears the table after the default fill.
			tc.setup(&d, panes)
			d = withWindows(d, testPane())
			r, err := New(d)
			if err != nil {
				t.Fatal(err)
			}
			res := r.Run(context.Background(), quickCmd(testSpec(checkout), testPane()), nil)
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

func TestWriteGoalFileRefusesEmptyWorktree(t *testing.T) {
	t.Parallel()
	d, typer, _, _, _, _ := testDeps(t, reusePanes(""))
	d = withWindows(d, testPane())
	r, err := New(d)
	if err != nil {
		t.Fatal(err)
	}
	spec := testSpec("")
	st := &state{cmd: quickCmd(spec, nil), spec: spec, goal: "goal1"}
	res := r.writeGoalFile(context.Background(), st)
	if res == nil || res.GetStatus() != typev1.Status_STATUS_FAILED ||
		res.GetReason() != typev1.ReasonCode_REASON_CODE_GOAL_FILE_WRITE_FAILED {
		t.Fatalf("writeGoalFile = %v; want FAILED GOAL_FILE_WRITE_FAILED", res)
	}
	if len(typer.calls) != 0 {
		t.Fatalf("typed %+v after a refusal", typer.calls)
	}
}

func TestWaitReadyOutcomes(t *testing.T) {
	t.Parallel()
	trust := "Accessing workspace:\nYes, I trust this folder"
	startup := "Codex\nUpdate available\n1. Update now"
	cases := []struct {
		name    string
		harness string
		screens []string
		reason  typev1.ReasonCode
		meta    map[string]string
	}{
		{
			name: "trust fails fast", harness: "claude", screens: []string{trust},
			reason: typev1.ReasonCode_REASON_CODE_TRUST_PROMPT_SEEN,
		},
		{
			name: "startup fails with kind", harness: "codex", screens: []string{startup},
			reason: typev1.ReasonCode_REASON_CODE_STARTUP_PROMPT_SEEN,
			meta:   map[string]string{"kind": "update"},
		},
		{
			name: "never ready times out", harness: "claude", screens: []string{busyScreen()},
			reason: typev1.ReasonCode_REASON_CODE_READY_TIMEOUT,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
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
			spec := testSpec(checkout)
			spec.Harness = &typev1.Harness{Name: tc.harness}
			screens.screens = tc.screens
			r, err := New(d)
			if err != nil {
				t.Fatal(err)
			}
			// New-window path: pick_session never runs, so wait_ready
			// reads the screens above.
			res := r.Run(context.Background(), quickCmd(spec, nil), nil)
			if res.GetStatus() != typev1.Status_STATUS_FAILED || res.GetReason() != tc.reason {
				t.Fatalf("Run = %v %v %q; want FAILED %v",
					res.GetStatus(), res.GetReason(), res.GetMessage(), tc.reason)
			}
			for k, v := range tc.meta {
				if res.GetMetadata()[k] != v {
					t.Fatalf("metadata = %v; want %v", res.GetMetadata(), tc.meta)
				}
			}
			if len(typer.calls) != 0 {
				t.Fatalf("typed %+v after a refusal", typer.calls)
			}
			if tc.reason == typev1.ReasonCode_REASON_CODE_READY_TIMEOUT && !res.GetRetryable() {
				t.Fatal("READY_TIMEOUT must be retryable")
			}
		})
	}
}
