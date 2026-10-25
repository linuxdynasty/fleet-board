package hookreconcile

import (
	"testing"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

func TestSnapshotInput(t *testing.T) {
	t.Parallel()
	snap := &agentv1.Snapshot{
		Harnesses: []*agentv1.HarnessInfo{
			{
				Harness: &typev1.Harness{Name: "codex"}, LaunchAllowed: true,
			},
			{
				Harness: &typev1.Harness{Name: "claude"}, LaunchAllowed: true,
				GoalHooks: &typev1.GoalHooksState{Installed: true, Verified: true, HookVersion: "v3"},
			},
		},
		Repos: []*agentv1.RepoCheckout{
			{
				Repo: &typev1.RepoRef{Host: "github.com", Path: "acme/web"},
			},
		},
		GoalRegistry: []*agentv1.GoalRegistration{
			{Goal: &typev1.GoalId{Value: "01aaa"}, Harness: &typev1.Harness{Name: "claude"}},
		},
	}
	repos := []Repo{{Host: "github.com", Path: "acme/web", Enabled: true, Mode: hubv1.RepoHooksMode_REPO_HOOKS_MODE_GENERIC}}
	got := Decide(SnapshotInput(snap, repos, map[string]string{"codex": "v3", "claude": "v3"}, nil, time.Now(), 0))
	want := []Want{{Key: Key{Harness: "codex", RepoHost: "github.com", RepoPath: "acme/web"}, Reason: "missing"}}
	if len(got) != 1 || got[0] != want[0] {
		t.Fatalf("Decide = %+v, want %+v", got, want)
	}
}

func TestCommandFor(t *testing.T) {
	t.Parallel()
	cmd := CommandFor(Want{Key: Key{Harness: "codex", Profile: "work", RepoHost: "github.com", RepoPath: "acme/web"}}, "v3")
	in := cmd.GetEnsureGoalHooks()
	if in == nil {
		t.Fatalf("not an EnsureGoalHooks command: %+v", cmd)
	}
	if in.GetHarness().GetName() != "codex" || in.GetProfile() != "work" || in.GetExpectedVersion() != "v3" {
		t.Fatalf("command = %+v", in)
	}
	if in.GetWorkdir().GetRepo().GetHost() != "github.com" || in.GetWorkdir().GetRepo().GetPath() != "acme/web" {
		t.Fatalf("workdir = %+v", in.GetWorkdir())
	}
	if in.GetWorkdir().GetRelativePath() != "" {
		t.Fatalf("hub never sends a machine path: %+v", in.GetWorkdir())
	}
}

func TestLiveHarnesses(t *testing.T) {
	t.Parallel()
	live := LiveHarnesses([]*agentv1.GoalRegistration{
		{Goal: &typev1.GoalId{Value: "01aaa"}, Harness: &typev1.Harness{Name: "claude"}},
		{Goal: &typev1.GoalId{Value: "01bbb"}},
	})
	if !live["claude"] || len(live) != 1 {
		t.Fatalf("live = %+v", live)
	}
}
