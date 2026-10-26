package startgoal

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

func seedRegistry(t *testing.T, d Deps, pane *typev1.PaneKey, goal, wt string, driver bool) {
	t.Helper()
	if _, err := d.Registry.Register(pane, goal, "claude", wt, driver); err != nil {
		t.Fatal(err)
	}
}

func TestRearm(t *testing.T) {
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
	seedRegistry(t, d, testPane(), "goal1", checkout, false)
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
	cmd := quickCmd(spec, testPane())
	cmd.RearmReason = "still broken"
	res := r.Run(context.Background(), cmd, rep)
	if res.GetStatus() != typev1.Status_STATUS_OK {
		t.Fatalf("Run = %v %q", res.GetStatus(), res.GetMessage())
	}
	want := []string{"preflight", "wait_ready", "write_goal_file", "type_goal", "confirm_goal_file"}
	if len(got) != len(want) {
		t.Fatalf("steps = %v; want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("steps = %v; want %v", got, want)
		}
	}
	if len(typer.calls) != 1 {
		t.Fatalf("typed = %+v; want one goal type", typer.calls)
	}
	raw, err := os.ReadFile(filepath.Join(checkout, ".fleet-board", "goal-goal1")) //nolint:gosec // G304: the goal file this run wrote in a temp dir
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "reopened: still broken") {
		t.Fatalf("goal file = %q; want reopened reason", raw)
	}
}

func TestRearmOverDoneFile(t *testing.T) {
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
	seedRegistry(t, d, testPane(), "goal1", checkout, false)
	if _, err := d.Files.EnsureGoalFile(checkout, "goal1",
		[]byte("DONE\nCONSENT: push\n"), false); err != nil {
		t.Fatal(err)
	}
	d = withWindows(d, testPane())
	screens.screens = []string{readyScreen()}
	r, err := New(d)
	if err != nil {
		t.Fatal(err)
	}
	spec := testSpec(checkout)
	cmd := quickCmd(spec, testPane())
	cmd.RearmReason = "still broken"
	res := r.Run(context.Background(), cmd, nil)
	if res.GetStatus() != typev1.Status_STATUS_OK {
		t.Fatalf("Run = %v %v %q; want OK", res.GetStatus(), res.GetReason(), res.GetMessage())
	}
	if len(typer.calls) != 1 {
		t.Fatalf("typed = %+v; want one goal retype", typer.calls)
	}
	raw, err := os.ReadFile(filepath.Join(checkout, ".fleet-board", "goal-goal1")) //nolint:gosec // G304: the goal file this run wrote in a temp dir
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "reopened: still broken") {
		t.Fatalf("goal file = %q; want reopened reason", raw)
	}
}

func TestRearmWrongPane(t *testing.T) {
	t.Parallel()
	panes := reusePanes("")
	d, typer, _, _, _, tmp := testDeps(t, panes)
	checkout := tmp + "/checkout"
	panes.repos["github.com/acme/repo"] = checkout
	other := &typev1.PaneKey{Server: "default", ServerStartId: "s1", PaneId: "%9"}
	seedRegistry(t, d, testPane(), "goal1", checkout, false)
	d = withWindows(d, testPane())
	r, err := New(d)
	if err != nil {
		t.Fatal(err)
	}
	cmd := quickCmd(testSpec(checkout), other)
	cmd.RearmReason = "still broken"
	res := r.Run(context.Background(), cmd, nil)
	if res.GetStatus() != typev1.Status_STATUS_FAILED ||
		res.GetReason() != typev1.ReasonCode_REASON_CODE_SESSION_NOT_AVAILABLE {
		t.Fatalf("Run = %v %v %q; want SESSION_NOT_AVAILABLE",
			res.GetStatus(), res.GetReason(), res.GetMessage())
	}
	if len(typer.calls) != 0 {
		t.Fatalf("typed %+v after a refusal", typer.calls)
	}
}

func TestResume(t *testing.T) {
	t.Parallel()
	t.Run("type goal without proof is unknown", func(t *testing.T) {
		t.Parallel()
		panes := reusePanes("")
		d, typer, _, _, _, tmp := testDeps(t, panes)
		checkout := tmp + "/checkout"
		for k, ps := range panes.states {
			ps.PaneDir, ps.CheckoutTop = checkout, checkout
			panes.states[k] = ps
		}
		panes.repos["github.com/acme/repo"] = checkout
		d = withWindows(d, testPane())
		r, err := New(d)
		if err != nil {
			t.Fatal(err)
		}
		res := r.Resume(context.Background(), quickCmd(testSpec(checkout), testPane()),
			stepIndex("type_goal"), nil)
		if res.GetStatus() != typev1.Status_STATUS_UNKNOWN_OUTCOME {
			t.Fatalf("Resume = %v %v %q; want UNKNOWN_OUTCOME",
				res.GetStatus(), res.GetReason(), res.GetMessage())
		}
		if res.GetRetryable() {
			t.Fatal("UNKNOWN_OUTCOME must never be retryable")
		}
		if len(typer.calls) != 0 {
			t.Fatalf("typed %+v on resume without proof", typer.calls)
		}
	})
	t.Run("type goal with a pre-typing file is unknown", func(t *testing.T) {
		t.Parallel()
		panes := reusePanes("")
		d, typer, _, _, _, tmp := testDeps(t, panes)
		checkout := tmp + "/checkout"
		for k, ps := range panes.states {
			ps.PaneDir, ps.CheckoutTop = checkout, checkout
			panes.states[k] = ps
		}
		panes.repos["github.com/acme/repo"] = checkout
		panes.push["github.com/acme/repo"] = true
		seedRegistry(t, d, testPane(), "goal1", checkout, false)
		// Step 12 wrote the file but step 13 never typed: the file alone
		// must not pass for proof of typing (crash between write and type).
		if _, err := d.Files.EnsureGoalFile(checkout, "goal1",
			[]byte("migrate the parser\nCONSENT: push\n"), false); err != nil {
			t.Fatal(err)
		}
		d = withWindows(d, testPane())
		r, err := New(d)
		if err != nil {
			t.Fatal(err)
		}
		res := r.Resume(context.Background(), quickCmd(testSpec(checkout), testPane()),
			stepIndex("type_goal"), nil)
		if res.GetStatus() != typev1.Status_STATUS_UNKNOWN_OUTCOME {
			t.Fatalf("Resume = %v %v %q; want UNKNOWN_OUTCOME",
				res.GetStatus(), res.GetReason(), res.GetMessage())
		}
		if len(typer.calls) != 0 {
			t.Fatalf("typed %+v on resume without typing proof", typer.calls)
		}
	})
	t.Run("type goal with typed marker skips retyping", func(t *testing.T) {
		t.Parallel()
		panes := reusePanes("")
		d, typer, _, _, reg, tmp := testDeps(t, panes)
		checkout := tmp + "/checkout"
		for k, ps := range panes.states {
			ps.PaneDir, ps.CheckoutTop = checkout, checkout
			panes.states[k] = ps
		}
		panes.repos["github.com/acme/repo"] = checkout
		panes.push["github.com/acme/repo"] = true
		seedRegistry(t, d, testPane(), "goal1", checkout, false)
		if err := d.Files.RewriteGoalFile(checkout, "goal1",
			[]byte("migrate the parser\nCONSENT: push\nTYPED: yes\n")); err != nil {
			t.Fatal(err)
		}
		d = withWindows(d, testPane())
		r, err := New(d)
		if err != nil {
			t.Fatal(err)
		}
		res := r.Resume(context.Background(), quickCmd(testSpec(checkout), testPane()),
			stepIndex("type_goal"), nil)
		if res.GetStatus() != typev1.Status_STATUS_OK {
			t.Fatalf("Resume = %v %v %q; want OK",
				res.GetStatus(), res.GetReason(), res.GetMessage())
		}
		if len(typer.calls) != 0 {
			t.Fatalf("retyped %+v after proof", typer.calls)
		}
		if _, ok := reg.ByPane(testPane()); !ok {
			t.Fatal("registry entry lost across resume")
		}
	})
	t.Run("clear context without proof is unknown", func(t *testing.T) {
		t.Parallel()
		panes := reusePanes("")
		d, _, _, _, _, tmp := testDeps(t, panes)
		checkout := tmp + "/checkout"
		for k, ps := range panes.states {
			ps.PaneDir, ps.CheckoutTop = checkout, checkout
			panes.states[k] = ps
		}
		panes.repos["github.com/acme/repo"] = checkout
		d = withWindows(d, testPane())
		r, err := New(d)
		if err != nil {
			t.Fatal(err)
		}
		spec := testSpec(checkout)
		spec.FreshContext = true
		res := r.Resume(context.Background(), quickCmd(spec, testPane()),
			stepIndex("clear_context"), nil)
		if res.GetStatus() != typev1.Status_STATUS_UNKNOWN_OUTCOME {
			t.Fatalf("Resume = %v %q; want UNKNOWN_OUTCOME", res.GetStatus(), res.GetMessage())
		}
	})
	t.Run("resume before register reruns declarative steps", func(t *testing.T) {
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
		screens.screens = []string{readyScreen()}
		r, err := New(d)
		if err != nil {
			t.Fatal(err)
		}
		res := r.Resume(context.Background(), quickCmd(testSpec(checkout), testPane()),
			stepIndex("register_goal"), nil)
		if res.GetStatus() != typev1.Status_STATUS_OK {
			t.Fatalf("Resume = %v %v %q; want OK",
				res.GetStatus(), res.GetReason(), res.GetMessage())
		}
		if len(typer.calls) != 1 {
			t.Fatalf("typed = %+v; want one goal type", typer.calls)
		}
	})
}
