package startgoal

import (
	"context"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/goalfile"
	"github.com/linuxdynasty/fleet-board/internal/agent/goalregistry"
)

// Step names in 02h order. StepIndex is the 1-based position in this
// list and StepCount is always len(steps), so a progress bar is stable
// when steps are skipped (a skipped step reports nothing).
var steps = []string{
	"preflight",         // 1
	"pick_session",      // 2
	"prepare_branch",    // 3
	"ensure_worktree",   // 4
	"ensure_window",     // 5
	"register_goal",     // 6
	"start_log_capture", // 7
	"wait_ready",        // 8
	"set_model",         // 9
	"set_effort",        // 10
	"clear_context",     // 11
	"write_goal_file",   // 12
	"type_goal",         // 13
	"confirm_goal_file", // 14
}

// stepIndex returns the 1-based index of a step name.
func stepIndex(name string) int {
	for i, s := range steps {
		if s == name {
			return i + 1
		}
	}
	return 0
}

// Non-idempotent steps are never retyped after a restart: a crash
// between their start and their proof ends UNKNOWN_OUTCOME (02h).
// type_goal is the exception the file proves: when the goal file already
// shows this goal's id, the type is turned into OK and the run continues
// at confirm_goal_file instead of typing again.
func nonIdempotent(name string) bool {
	return name == "clear_context" || name == "type_goal"
}

// PaneState is what the runner knows about one pane: the inventory
// snapshot row plus the registry binding and the checkout facts the
// pick_session table needs (02h). Production fills it in
// cmd/fleet-agent/goalwire.go; tests fake it.
type PaneState struct {
	Exists       bool
	PaneDir      string
	CheckoutTop  string
	Dirty        bool
	Harness      string
	Ready        bool
	InputEmpty   bool
	InputDraft   bool
	TrustBox     bool
	StartupKind  string
	BoundGoal    string
	CheckoutHeld bool
	FleetSession bool
	FreshMark    bool
	Session      *agentv1.TmuxSession
}

// Panes reads pane and repo facts. LookupRepo resolves a RepoRef to its
// local checkout top (CHECKOUT_MISSING when absent); PushAccess reports
// the H4 push access for a repo (known false when gh cannot answer, so
// the hub's fresher reading rules and the agent never blocks on it).
type Panes interface {
	Examine(ctx context.Context, pane *typev1.PaneKey) (PaneState, error)
	LookupRepo(repo *typev1.RepoRef) (string, error)
	PushAccess(ctx context.Context, repo *typev1.RepoRef) (push, known bool)
}

// Git starts branches in place for reused panes.
type Git interface {
	// SwitchBranch starts branch (from base when set) in dir's checkout.
	// Already on branch is OK. Returns HEAD.
	SwitchBranch(ctx context.Context, dir, branch, base string) (string, error)
}

// Trees is the row-20 worktree manager surface StartGoal needs: a fresh
// worktree per goal (AGENT_ENSURES), or a scratch directory for
// SCRATCH goals. Failures arrive as the manager's own CommandResult
// (WORKTREE_EXISTS, GIT_FAILED, REPO_ROOT_NOT_ALLOWED).
type Trees interface {
	EnsureWorktree(ctx context.Context, cmd *agentv1.EnsureWorktree) *agentv1.CommandResult
	EnsureScratchDir(ctx context.Context, goal string) (string, *agentv1.CommandResult)
}

// Window is one opened goal window.
type Window struct {
	Pane    *typev1.PaneKey
	Session *agentv1.TmuxSession
	Reused  bool
}

// Windows opens goal windows: a new window in the target session (a new
// session named session when the target is empty) running the harness
// with FLEET_GOAL_ID, tagged with the goal id so a resumed run reattaches
// instead of opening a second window. A nil result is OK.
type Windows interface {
	Start(ctx context.Context, target *agentv1.SessionTarget, session, window, dir, harness, goal, profile string) (*Window, *agentv1.CommandResult)
}

// Typer types one slash command through RunHarnessCommand at an idle
// prompt with an empty input line. A nil result is OK; anything else is
// the typer's refusal (PANE_BUSY, CATALOG_STALE, ...), which the step
// passes through unchanged.
type Typer interface {
	Type(ctx context.Context, pane *typev1.PaneKey, command string, class typev1.CommandClass, args string) *agentv1.CommandResult
}

// Screens reads styled screens (escapes kept: the placeholder and trust
// checks need them).
type Screens interface {
	Capture(ctx context.Context, server, pane string) (string, error)
}

// Capture starts pipe-pane capture of the goal pane into the agent's
// raw files (K1). Shipping and logs.db are row 25's.
type Capture interface {
	Start(ctx context.Context, pane *typev1.PaneKey, goal string) error
}

// Deps wires a Runner. Registry and Files are the real in-memory
// registry and the real no-follow writer (both deterministic without a
// fleet); every other seam is faked in tests. MaxGoals is the local
// goal cap (AGENT_GOAL_CAP_REACHED, 03e max_total, default 4);
// DiskFree reports free bytes on the state volume (ok false when
// unreadable); PollReady and PollFile bound the wait loops.
type Deps struct {
	Panes     Panes
	Git       Git
	Trees     Trees
	Windows   Windows
	Registry  *goalregistry.Registry
	Files     goalfile.Writer
	Typer     Typer
	Screens   Screens
	Capture   Capture
	MaxGoals  func() int
	DiskFree  func() (int64, bool)
	Now       func() time.Time
	PollReady time.Duration
	PollFile  time.Duration
}

// Runner runs the 14 steps.
type Runner struct {
	d Deps
}

// New checks the seams: a half-wired Runner must never answer.
func New(d Deps) (*Runner, error) {
	switch {
	case d.Panes == nil:
		return nil, errSeam("Panes")
	case d.Git == nil:
		return nil, errSeam("Git")
	case d.Trees == nil:
		return nil, errSeam("Trees")
	case d.Windows == nil:
		return nil, errSeam("Windows")
	case d.Registry == nil:
		return nil, errSeam("Registry")
	case d.Typer == nil:
		return nil, errSeam("Typer")
	case d.Screens == nil:
		return nil, errSeam("Screens")
	case d.Capture == nil:
		return nil, errSeam("Capture")
	case d.MaxGoals == nil:
		return nil, errSeam("MaxGoals")
	case d.DiskFree == nil:
		return nil, errSeam("DiskFree")
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.PollReady <= 0 {
		d.PollReady = 500 * time.Millisecond
	}
	if d.PollFile <= 0 {
		d.PollFile = 250 * time.Millisecond
	}
	return &Runner{d: d}, nil
}
