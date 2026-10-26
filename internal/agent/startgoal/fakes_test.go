package startgoal

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/goalfile"
	"github.com/linuxdynasty/fleet-board/internal/agent/goalregistry"
)

// fakePanes answers Examine from a table, LookupRepo from a map, and
// PushAccess from a map (known only when present).
type fakePanes struct {
	states map[string]PaneState
	repos  map[string]string
	push   map[string]bool
	err    error
}

func paneID(k *typev1.PaneKey) string {
	return k.GetServer() + "\x00" + k.GetServerStartId() + "\x00" + k.GetPaneId()
}

func (f *fakePanes) Examine(_ context.Context, pane *typev1.PaneKey) (PaneState, error) {
	if f.err != nil {
		return PaneState{}, f.err
	}
	ps, ok := f.states[paneID(pane)]
	if !ok {
		return PaneState{Exists: false}, nil
	}
	return ps, nil
}

func (f *fakePanes) LookupRepo(repo *typev1.RepoRef) (string, error) {
	if top, ok := f.repos[repo.GetHost()+"/"+repo.GetPath()]; ok {
		return top, nil
	}
	return "", fmt.Errorf("no checkout")
}

func (f *fakePanes) PushAccess(_ context.Context, repo *typev1.RepoRef) (bool, bool) {
	push, ok := f.push[repo.GetHost()+"/"+repo.GetPath()]
	return push, ok
}

type fakeGit struct {
	head  string
	err   error
	calls []string
}

func (f *fakeGit) SwitchBranch(_ context.Context, dir, branch, _ string) (string, error) {
	f.calls = append(f.calls, dir+"\x00"+branch)
	if f.err != nil {
		return "", f.err
	}
	return f.head, nil
}

type fakeTrees struct {
	wtPath  string
	created bool
	head    string
	wtRes   *agentv1.CommandResult
	scratch string
	scrRes  *agentv1.CommandResult
}

func (f *fakeTrees) EnsureWorktree(_ context.Context, _ *agentv1.EnsureWorktree) *agentv1.CommandResult {
	if f.wtRes != nil {
		return f.wtRes
	}
	return &agentv1.CommandResult{
		Status: typev1.Status_STATUS_OK,
		Detail: &agentv1.CommandResult_EnsureWorktree{EnsureWorktree: &agentv1.EnsureWorktreeResult{
			Path: f.wtPath, Created: f.created,
			Branch: &agentv1.BranchState{Branch: "fleet/1", HeadCommit: f.head},
		}},
	}
}

func (f *fakeTrees) EnsureScratchDir(_ context.Context, _ string) (string, *agentv1.CommandResult) {
	return f.scratch, f.scrRes
}

type typedCall struct {
	pane    *typev1.PaneKey
	command string
	class   typev1.CommandClass
	args    string
}

type fakeTyper struct {
	calls []typedCall
	res   *agentv1.CommandResult
}

func (f *fakeTyper) Type(_ context.Context, pane *typev1.PaneKey, command string, class typev1.CommandClass, args string) *agentv1.CommandResult {
	f.calls = append(f.calls, typedCall{pane: pane, command: command, class: class, args: args})
	return f.res
}

type fakeScreens struct {
	screens []string
	err     error
	calls   int
}

func (f *fakeScreens) Capture(_ context.Context, _, _ string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	f.calls++
	if len(f.screens) == 0 {
		return "", fmt.Errorf("no screen")
	}
	if f.calls <= len(f.screens) {
		return f.screens[f.calls-1], nil
	}
	return f.screens[len(f.screens)-1], nil
}

type fakeCapture struct {
	started []string
	err     error
}

func (f *fakeCapture) Start(_ context.Context, pane *typev1.PaneKey, goal string) error {
	f.started = append(f.started, goal)
	return f.err
}

func testPane() *typev1.PaneKey {
	return &typev1.PaneKey{Server: "default", ServerStartId: "s1", PaneId: "%1"}
}

func testSpec(top string) *agentv1.GoalSpec {
	return &agentv1.GoalSpec{
		GoalId:       &typev1.GoalId{Value: "goal1"},
		Harness:      &typev1.Harness{Name: "claude"},
		Repo:         &typev1.RepoRef{Host: "github.com", Path: "acme/repo"},
		Consent:      []typev1.ConsentAct{typev1.ConsentAct_CONSENT_ACT_PUSH},
		GoalText:     "migrate the parser",
		WorktreeMode: agentv1.WorktreeMode_WORKTREE_MODE_AGENT_ENSURES,
		Branch:       "fleet/1",
		WorkItem:     &typev1.WorkItemRef{Display: "eshu#7398"},
	}
}

// testDeps wires a Runner with real registry and goal files in temp
// dirs; every external seam is fake.
func testDeps(t *testing.T, panes *fakePanes) (Deps, *fakeTyper, *fakeScreens, *fakeCapture, *goalregistry.Registry, string) {
	t.Helper()
	tmp := t.TempDir()
	top := filepath.Join(tmp, "checkout")
	if err := os.MkdirAll(top, 0o700); err != nil {
		t.Fatal(err)
	}
	board := func(workdir string) (string, bool, error) {
		bd := filepath.Join(workdir, ".fleet-board")
		if err := os.MkdirAll(bd, 0o700); err != nil {
			return "", false, err
		}
		if err := os.WriteFile(filepath.Join(bd, ".gitignore"),
			[]byte("# fleet-board goal files; this folder ignores itself\n*\n"), 0o600); err != nil {
			return "", false, err
		}
		return bd, false, nil
	}
	reg := goalregistry.New(nil, nil)
	typer := &fakeTyper{}
	screens := &fakeScreens{}
	cap := &fakeCapture{}
	return Deps{
		Panes: panes, Git: &fakeGit{head: "abc123"},
		Trees:    &fakeTrees{wtPath: filepath.Join(tmp, "wt-goal1"), head: "def456"},
		Windows:  nil, // set per test (needs tmp)
		Registry: reg, Files: goalfile.Writer{EnsureBoard: board},
		Typer: typer, Screens: screens, Capture: cap,
		MaxGoals:  func() int { return 4 },
		DiskFree:  func() (int64, bool) { return 100 << 30, true },
		Now:       time.Now,
		PollReady: time.Millisecond, PollFile: time.Millisecond,
	}, typer, screens, cap, reg, tmp
}

func readyScreen() string {
	return "Claude Code v2.1.286\n\n❯ "
}

func busyScreen() string {
	return "Claude Code v2.1.286\n\n❯ typed text here"
}
