package hooks

import (
	"context"
	"errors"
	"testing"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	agentexec "github.com/linuxdynasty/fleet-board/internal/agent/exec"
)

// TestEnsureGoalHooksHandlerInstallsAndVerifies is the row-20 G1 seam:
// the hub's EnsureGoalHooks command runs install+verify in a checkout
// and reports the hook state back.
func TestEnsureGoalHooksHandlerInstallsAndVerifies(t *testing.T) {
	var gotHarness, gotWorkdir, gotExpected string
	h := NewHandler(Config{
		Home:    "/home/dev",
		DataDir: t.TempDir(),
		Lookup: func(repo *typev1.RepoRef) (string, error) {
			if repo.GetHost() != "github.com" || repo.GetPath() != "o/r" {
				t.Fatalf("lookup repo = %v, want github.com/o/r", repo)
			}
			return t.TempDir(), nil
		},
		Ensure: func(home, dataDir, harness, workdir, expected string) (*typev1.GoalHooksState, error) {
			gotHarness, gotWorkdir, gotExpected = harness, workdir, expected
			return &typev1.GoalHooksState{Installed: true, Verified: true, HookVersion: "9"}, nil
		},
	})
	res := h.Run(context.Background(), agentexec.Request{Command: &agentv1.Command{
		Kind: &agentv1.Command_EnsureGoalHooks{EnsureGoalHooks: &agentv1.EnsureGoalHooks{
			Harness:         &typev1.Harness{Name: "claude"},
			Workdir:         &typev1.Workdir{Repo: &typev1.RepoRef{Host: "github.com", Path: "o/r"}},
			ExpectedVersion: "1",
		}},
	}})
	if res.GetStatus() != typev1.Status_STATUS_OK {
		t.Fatalf("Run status = %v, want OK (%s)", res.GetStatus(), res.GetMessage())
	}
	got := res.GetEnsureGoalHooksResult()
	if got == nil || !got.GetState().GetInstalled() || !got.GetState().GetVerified() {
		t.Fatalf("Run result = %v, want installed+verified state", res)
	}
	if gotHarness != "claude" || gotExpected != "1" || gotWorkdir == "" {
		t.Fatalf("Ensure args = %q %q %q, want claude, workdir, 1", gotHarness, gotWorkdir, gotExpected)
	}
}

// TestEnsureGoalHooksHandlerRefusesUnknownRepo: a repo with no checkout
// is CHECKOUT_MISSING, never a guess.
func TestEnsureGoalHooksHandlerRefusesUnknownRepo(t *testing.T) {
	h := NewHandler(Config{
		Home: "/home/dev", DataDir: t.TempDir(),
		Lookup: func(*typev1.RepoRef) (string, error) { return "", errors.New("no checkout") },
		Ensure: func(home, dataDir, harness, workdir, expected string) (*typev1.GoalHooksState, error) {
			t.Fatal("Ensure must not run without a checkout")
			return nil, nil
		},
	})
	res := h.Run(context.Background(), agentexec.Request{Command: &agentv1.Command{
		Kind: &agentv1.Command_EnsureGoalHooks{EnsureGoalHooks: &agentv1.EnsureGoalHooks{
			Harness: &typev1.Harness{Name: "claude"},
			Workdir: &typev1.Workdir{Repo: &typev1.RepoRef{Host: "github.com", Path: "o/gone"}},
		}},
	}})
	if res.GetStatus() != typev1.Status_STATUS_FAILED ||
		res.GetReason() != typev1.ReasonCode_REASON_CODE_CHECKOUT_MISSING {
		t.Fatalf("Run = %v %v, want FAILED CHECKOUT_MISSING", res.GetStatus(), res.GetReason())
	}
}

// TestEnsureGoalHooksHandlerRefusesWithoutHome: with no home folder the
// user config has nowhere to go, so the command refuses.
func TestEnsureGoalHooksHandlerRefusesWithoutHome(t *testing.T) {
	h := NewHandler(Config{DataDir: t.TempDir()})
	res := h.Run(context.Background(), agentexec.Request{Command: &agentv1.Command{
		Kind: &agentv1.Command_EnsureGoalHooks{EnsureGoalHooks: &agentv1.EnsureGoalHooks{
			Harness: &typev1.Harness{Name: "claude"},
		}},
	}})
	if res.GetStatus() != typev1.Status_STATUS_FAILED {
		t.Fatalf("Run without home = %v, want FAILED", res.GetStatus())
	}
}

// TestEnsureGoalHooksHandlerCapabilities: one handler advertises all four
// hook shapes at version 1 (02e goal.hooks.<harness>).
func TestEnsureGoalHooksHandlerCapabilities(t *testing.T) {
	h := NewHandler(Config{})
	want := map[string]bool{}
	for _, c := range h.Capabilities() {
		if c.GetVersion() != 1 {
			t.Fatalf("capability %v version = %d, want 1", c.GetName(), c.GetVersion())
		}
		want[c.GetName()] = true
	}
	for _, name := range []string{"goal.hooks.claude", "goal.hooks.codex", "goal.hooks.muse", "goal.hooks.cursor"} {
		if !want[name] {
			t.Fatalf("capabilities = %v, missing %s", want, name)
		}
	}
}
