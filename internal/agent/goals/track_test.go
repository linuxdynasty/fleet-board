package goals_test

import (
	"strings"
	"testing"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/repos"
)

// A goal file in a pane's folder yields one observed goal with the state.
func TestScanFindsObservedGoal(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.writeGoal("Work issue acme/repo#12: fix the leak.\nCONSENT: push\n", r.now.Add(-time.Minute))
	r.scan()

	got := r.goals()
	if len(got) != 1 {
		t.Fatalf("goals = %d, want 1", len(got))
	}
	g := got[0]
	if !strings.HasPrefix(g.GetGoalId().GetValue(), "obs-") {
		t.Fatalf("id = %q, want an agent-minted obs- id", g.GetGoalId().GetValue())
	}
	if g.GetBinding() != agentv1.GoalBindingSource_GOAL_BINDING_SOURCE_OBSERVED {
		t.Fatalf("binding = %v, want OBSERVED", g.GetBinding())
	}
	if g.GetState() != typev1.GoalLineState_GOAL_LINE_STATE_WORKING {
		t.Fatalf("state = %v, want WORKING", g.GetState())
	}
	if g.GetFirstLine() != "Work issue acme/repo#12: fix the leak." {
		t.Fatalf("first = %q", g.GetFirstLine())
	}
	if g.GetConsentLine() != "CONSENT: push" {
		t.Fatalf("consent = %q", g.GetConsentLine())
	}
	if g.GetWorktreePath() != r.dir || g.GetGoalFile() != ".claude/active-goal.s1" {
		t.Fatalf("worktree = %q file = %q", g.GetWorktreePath(), g.GetGoalFile())
	}
	if g.GetBranch() != "fleet/12" || g.GetHeadCommit() != "abc123" || g.GetDirty() {
		t.Fatalf("branch = %q head = %q dirty = %v", g.GetBranch(), g.GetHeadCommit(), g.GetDirty())
	}
	if g.GetPane().GetPaneId() != "%1" {
		t.Fatalf("pane = %v, want %%1", g.GetPane())
	}
	if len(r.emitted) != 0 {
		t.Fatalf("first scan emitted %d events, want none (the snapshot carries it)", len(r.emitted))
	}
	if len(r.tr.Ambiguous()) != 0 {
		t.Fatalf("ambiguous = %q, want none", r.tr.Ambiguous())
	}
}

// A changed goal line emits one GoalState event; the watch flip too.
func TestScanEmitsOnChange(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.writeGoal("Working\n", r.now.Add(-time.Minute))
	r.scan()
	r.procs.alive[4242] = true
	r.writeGoal("BLOCKED: waiting on CI WATCH=4242\n", r.now.Add(-time.Second))
	r.scan()

	if len(r.emitted) != 1 {
		t.Fatalf("emitted = %d, want 1", len(r.emitted))
	}
	r.assertAllGoalState(t)
	g := r.emitted[0].GetGoalState()
	if g.GetState() != typev1.GoalLineState_GOAL_LINE_STATE_BLOCKED_WATCH || !g.GetWatchAlive() || g.GetWatchPid() != 4242 {
		t.Fatalf("event = %+v, want BLOCKED_WATCH alive pid 4242", g)
	}
	// A dead watcher flips watch_alive and emits again; the hub reads it
	// stuck (03k).
	delete(r.procs.alive, 4242)
	r.scan()
	if len(r.emitted) != 2 || r.emitted[1].GetGoalState().GetWatchAlive() {
		t.Fatalf("emitted = %d, want a second event with the watcher dead", len(r.emitted))
	}
	// No change emits nothing more.
	r.scan()
	if len(r.emitted) != 2 {
		t.Fatalf("emitted = %d, want no event without a change", len(r.emitted))
	}
}

// Two harness panes in one folder yield none, and doctor lists the folder.
func TestAmbiguousTwoPanes(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.writeGoal("Working\n", r.now.Add(-time.Minute))
	r.addPane("%2", 101, "codex", r.now.Add(-time.Hour))
	r.scan()

	if got := r.goals(); len(got) != 0 {
		t.Fatalf("goals = %d, want none (ambiguous, never guessed)", len(got))
	}
	amb := r.tr.Ambiguous()
	if len(amb) != 1 || amb[0] != r.dir {
		t.Fatalf("ambiguous = %q, want [%q]", amb, r.dir)
	}
}

// DONE ends the goal with one terminal event; a rewritten file is a new id.
func TestDoneEndsGoal(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.writeGoal("Working\n", r.now.Add(-time.Minute))
	r.scan()
	first := r.goals()[0].GetGoalId().GetValue()

	r.writeGoal("DONE\n", r.now.Add(-time.Second))
	r.scan()
	if got := r.goals(); len(got) != 0 {
		t.Fatalf("goals = %d after DONE, want none", len(got))
	}
	if len(r.emitted) != 1 {
		t.Fatalf("emitted = %d, want the terminal DONE event", len(r.emitted))
	}
	term := r.emitted[0].GetGoalState()
	if term == nil || term.GetState() != typev1.GoalLineState_GOAL_LINE_STATE_DONE {
		t.Fatalf("terminal = %+v, want DONE", r.emitted[0])
	}
	if term.GetGoalId().GetValue() != first {
		t.Fatalf("terminal id = %q, want %q", term.GetGoalId().GetValue(), first)
	}
	r.assertAllGoalState(t)

	// The next goal in the same file is a new episode with a new id (02h).
	r.now = r.now.Add(time.Minute)
	r.writeGoal("Working again\n", r.now.Add(-time.Second))
	r.scan()
	got := r.goals()
	if len(got) != 1 || got[0].GetGoalId().GetValue() == first {
		t.Fatalf("goals = %+v, want one goal with a new id", got)
	}
}

// A pane exit ends the goal silently: the snapshot drops it (OBSERVED_GONE).
func TestPaneExitEndsGoal(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.writeGoal("Working\n", r.now.Add(-time.Minute))
	r.scan()
	if len(r.goals()) != 1 {
		t.Fatal("no goal to exit")
	}
	r.panes.panes = nil
	r.scan() // first empty tick: kept (a hiccup must not end it)
	if len(r.goals()) != 1 {
		t.Fatal("one empty tick ended the goal")
	}
	r.scan() // second: pane exit ends it
	if len(r.goals()) != 0 {
		t.Fatal("pane exit did not end the goal")
	}
	if len(r.emitted) != 0 {
		t.Fatalf("emitted = %d, want no event for a silent end", len(r.emitted))
	}
	r.assertAllGoalState(t)
}

// A restart keeps the id: episodes live in agent.db, not memory.
func TestRestartKeepsID(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.writeGoal("Working\n", r.now.Add(-time.Minute))
	r.scan()
	first := r.goals()[0].GetGoalId().GetValue()

	r.retrack() // the agent restarted
	r.scan()
	got := r.goals()
	if len(got) != 1 || got[0].GetGoalId().GetValue() != first {
		t.Fatalf("goals = %+v, want the same id %q", got, first)
	}
}

// A file older than the harness start is a leftover: never attributed.
func TestStaleFileIgnored(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.writeGoal("Working\n", r.now.Add(-2*time.Hour)) // proc started an hour ago
	r.scan()
	if got := r.goals(); len(got) != 0 {
		t.Fatalf("goals = %+v, want none for a stale file", got)
	}
}

// DONE at first sight shows nothing and writes no episode.
func TestDoneAtFirstSight(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.writeGoal("DONE\n", r.now.Add(-time.Minute))
	r.scan()
	if got := r.goals(); len(got) != 0 {
		t.Fatalf("goals = %+v, want none", got)
	}
	if len(r.emitted) != 0 {
		t.Fatalf("emitted = %d, want none", len(r.emitted))
	}
}

// A dirty checkout and a detached HEAD are reported as they are.
func TestDirtyDetached(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.check.details[r.dir] = reposDetail(r.dir, "", "deadbee", true)
	r.writeGoal("Working\n", r.now.Add(-time.Minute))
	r.scan()
	got := r.goals()
	if len(got) != 1 {
		t.Fatalf("goals = %d, want 1", len(got))
	}
	if !got[0].GetDirty() || got[0].GetBranch() != "" || got[0].GetHeadCommit() != "deadbee" {
		t.Fatalf("goal = %+v, want dirty, no branch, head deadbee", got[0])
	}
}

// Fill binds the pane and the checkout branch to the goal.
func TestFillBindsPaneAndBranch(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.writeGoal("Working\n", r.now.Add(-time.Minute))
	r.scan()
	id := r.goals()[0].GetGoalId().GetValue()

	snap := &agentv1.Snapshot{
		TmuxServers: []*agentv1.TmuxServer{{
			Sessions: []*agentv1.TmuxSession{{
				Windows: []*agentv1.TmuxWindow{{
					Panes: []*agentv1.TmuxPane{{
						Key: &typev1.PaneKey{Server: "default", ServerStartId: "s1", PaneId: "%1"},
					}},
				}},
			}},
		}},
		Repos: []*agentv1.RepoCheckout{{
			Path:   r.dir,
			Branch: &agentv1.BranchState{Path: r.dir},
		}},
	}
	// The pane was located during the scan, so Fill reports its workdir.
	r.panes.loc["%1"] = loc{path: r.dir + "/pkg/x", pid: 100}
	r.scan()
	r.tr.Fill(snap)
	pane := snap.GetTmuxServers()[0].GetSessions()[0].GetWindows()[0].GetPanes()[0]
	if pane.GetGoal().GetValue() != id {
		t.Fatalf("pane goal = %v, want %q", pane.GetGoal(), id)
	}
	if pane.GetWorkdir().GetCheckoutPath() != r.dir {
		t.Fatalf("pane workdir = %v, want the checkout", pane.GetWorkdir())
	}
	if snap.GetRepos()[0].GetBranch().GetGoalId().GetValue() != id {
		t.Fatalf("branch goal = %v, want %q", snap.GetRepos()[0].GetBranch().GetGoalId(), id)
	}
}

func reposDetail(top, branch, head string, dirty bool) repos.Detail {
	return repos.Detail{Top: top, Branch: branch, Head: head, Dirty: dirty}
}
