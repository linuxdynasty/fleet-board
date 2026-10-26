package main

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/control"
	"github.com/linuxdynasty/fleet-board/internal/agent/goalfile"
	"github.com/linuxdynasty/fleet-board/internal/agent/spool"
	"github.com/linuxdynasty/fleet-board/internal/agent/startgoal"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux/inventory"
	"github.com/linuxdynasty/fleet-board/internal/harness"
	"github.com/linuxdynasty/fleet-board/internal/proc"
	"golang.org/x/sys/unix"
)

// This file holds the row-21 StartGoal command surface: branches,
// windows, typing, capture, and the E8 spool tailer. Pane facts live in
// startgoalwire.go.
// gitSwitch starts branches in place for reused panes.
type gitSwitch struct {
	runner control.Runner
	caller proc.Caller
}

func (s *gitSwitch) run(ctx context.Context, dir string, args ...string) (string, error) {
	res, err := s.runner.Run(ctx, proc.Spec{Caller: s.caller, Path: "git", Args: args, Dir: dir})
	if err != nil {
		return "", fmt.Errorf("git %s: %s: %w", strings.Join(args, " "), strings.TrimSpace(string(res.Stderr)), err)
	}
	return strings.TrimSpace(string(res.Stdout)), nil
}

// SwitchBranch starts branch in dir's checkout: already on it is OK and
// returns HEAD, a dirty checkout is a dirty error (CHECKOUT_DIRTY), and
// anything else is git's own failure (GIT_FAILED).
func (s *gitSwitch) SwitchBranch(ctx context.Context, dir, branch, base string) (string, error) {
	current, err := s.run(ctx, dir, "--no-optional-locks", "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", err
	}
	if current == branch {
		return s.run(ctx, dir, "--no-optional-locks", "rev-parse", "HEAD")
	}
	status, err := s.run(ctx, dir, "--no-optional-locks", "status", "--porcelain=v1", "--untracked-files=normal")
	if err != nil {
		return "", err
	}
	if status != "" {
		return "", fmt.Errorf("git switch -c %s: dirty checkout with uncommitted changes", branch)
	}
	args := []string{"--no-optional-locks", "switch", "-c", branch}
	if base != "" {
		args = append(args, base)
	}
	if _, err := s.run(ctx, dir, args...); err != nil {
		return "", err
	}
	return s.run(ctx, dir, "--no-optional-locks", "rev-parse", "HEAD")
}

// managerTrees adapts the row-20 worktree manager to the StartGoal
// surface: EnsureWorktree passes through, and the scratch directory is
// the documented <state>/scratch/<goal> the manager ensures (02h).
type managerTrees struct {
	mgr interface {
		EnsureWorktree(ctx context.Context, cmd *agentv1.EnsureWorktree) *agentv1.CommandResult
		EnsureScratch(ctx context.Context, cmd *agentv1.EnsureScratch) *agentv1.CommandResult
	}
	stateDir func() string
}

func (t *managerTrees) EnsureWorktree(ctx context.Context, cmd *agentv1.EnsureWorktree) *agentv1.CommandResult {
	return t.mgr.EnsureWorktree(ctx, cmd)
}

func (t *managerTrees) EnsureScratchDir(ctx context.Context, goal string) (string, *agentv1.CommandResult) {
	res := t.mgr.EnsureScratch(ctx, &agentv1.EnsureScratch{Goal: &typev1.GoalId{Value: goal}})
	if res.GetStatus() != typev1.Status_STATUS_OK {
		return "", res
	}
	return filepath.Join(t.stateDir(), "scratch", goal), nil
}

// goalEnsurers holds one control Ensurer per tmux server the agent
// serves (default and fleet; the probe server stays probes-only).
type goalEnsurers struct {
	g        *goalsRun
	byServer map[string]*control.Ensurer
}

func (g *goalsRun) ensurers() *goalEnsurers {
	ge := &goalEnsurers{g: g, byServer: map[string]*control.Ensurer{}}
	for _, name := range []string{"default", "fleet"} {
		kind := typev1.TmuxServerKind_TMUX_SERVER_KIND_DEFAULT
		if name == "fleet" {
			kind = typev1.TmuxServerKind_TMUX_SERVER_KIND_FLEET
		}
		client := g.tmx.obs.Client(name)
		if client == nil {
			continue
		}
		e, err := control.New(control.Options{
			Runner: g.runner, Caller: g.tmuxCaller, Tmux: g.tmx.program,
			Socket: filepath.Join(g.tmx.obs.Folder(), name), Server: name, Kind: kind,
			Roots: g.roots, ResolveWorkdir: resolveWorkdir(g),
			Sender: client, CheckInput: checkInput(g.tmx.obs, name), Marks: g.tmx.obs,
		})
		if err != nil {
			continue
		}
		ge.byServer[name] = e
	}
	return ge
}

// forPane picks the Ensurer serving the pane's server (empty means the
// default server, where the developer's sessions live).
func (ge *goalEnsurers) forPane(server string) *control.Ensurer {
	if server == "" {
		server = "default"
	}
	return ge.byServer[server]
}

func resolveWorkdir(g *goalsRun) func(*typev1.Workdir) (string, error) {
	return func(w *typev1.Workdir) (string, error) {
		g.mu.Lock()
		defer g.mu.Unlock()
		top, ok := g.refresher.LookupRepo(w.GetRepo().GetHost(), w.GetRepo().GetPath())
		if !ok {
			return "", fmt.Errorf("control: no local checkout")
		}
		return top, nil
	}
}

func checkInput(obs *inventory.Observer, server string) func(context.Context, string) (time.Time, error) {
	return func(ctx context.Context, pane string) (time.Time, error) {
		return obs.CheckInput(ctx, server, pane)
	}
}

// goalWindows opens goal windows through the serving Ensurer. The
// Ensurers build on every command (no I/O, checks only), so a server
// that appears later is served without a restart.
type goalWindows struct {
	g *goalsRun
}

func (w *goalWindows) ensure() *goalEnsurers {
	return w.g.ensurers()
}

func (w *goalWindows) Start(ctx context.Context, target *agentv1.SessionTarget, session, window, dir, harness, goal, profile string) (*startgoal.Window, *agentv1.CommandResult) {
	ge := w.ensure()
	server := target.GetServer()
	if server == typev1.TmuxServerKind_TMUX_SERVER_KIND_UNSPECIFIED {
		server = typev1.TmuxServerKind_TMUX_SERVER_KIND_DEFAULT
	}
	var picked *control.Ensurer
	for name, ens := range ge.byServer {
		want := typev1.TmuxServerKind_TMUX_SERVER_KIND_DEFAULT
		if name == "fleet" {
			want = typev1.TmuxServerKind_TMUX_SERVER_KIND_FLEET
		}
		if want == server {
			picked = ens
			break
		}
	}
	if picked == nil {
		return nil, &agentv1.CommandResult{
			Status: typev1.Status_STATUS_FAILED, Reason: typev1.ReasonCode_REASON_CODE_TMUX_SERVER_DOWN,
			Message: "startgoal: no control client serves that tmux server",
		}
	}
	out, res := picked.StartGoalWindow(ctx, control.GoalWindowArgs{
		Target: target, SessionName: sessionName(target, session),
		Window: window, Dir: dir, Harness: harness, GoalID: goal, Profile: profile,
	})
	if res != nil {
		return nil, res
	}
	return &startgoal.Window{
		Pane: out.Pane,
		Session: &agentv1.TmuxSession{
			Name: out.SessionName, StartedByFleet: true,
			SessionId: &typev1.SessionId{Value: out.SessionID},
		},
		Reused: !out.CreatedWindow,
	}, nil
}

// sessionName resolves the fleet session id for a new session: the
// target's, or the spec's when the target names nothing.
func sessionName(target *agentv1.SessionTarget, spec string) string {
	if target.GetSessionId().GetValue() != "" {
		return target.GetSessionId().GetValue()
	}
	if target.GetName() != "" {
		return target.GetName()
	}
	return spec
}

// goalTyper types slash commands through RunHarnessCommand at the
// serving Ensurer. The catalog hash comes from the goal bound to the
// pane: post-register the entry is the source of truth.
type goalTyper struct {
	g *goalsRun
}

func (t *goalTyper) ensure() *goalEnsurers {
	return t.g.ensurers()
}

func (t *goalTyper) Type(ctx context.Context, pane *typev1.PaneKey, command string, class typev1.CommandClass, args string) *agentv1.CommandResult {
	ge := t.ensure()
	e := ge.forPane(pane.GetServer())
	if e == nil {
		return &agentv1.CommandResult{
			Status: typev1.Status_STATUS_FAILED, Reason: typev1.ReasonCode_REASON_CODE_TMUX_SERVER_DOWN,
			Message: "startgoal: no control client serves that tmux server",
		}
	}
	entry, ok := t.g.reg.Registry().ByPane(pane)
	if !ok {
		return &agentv1.CommandResult{
			Status: typev1.Status_STATUS_FAILED, Reason: typev1.ReasonCode_REASON_CODE_INTERNAL,
			Message: "startgoal: no goal bound to that pane",
		}
	}
	return e.RunHarnessCommand(ctx, &agentv1.RunHarnessCommand{
		Pane: pane, Command: command, ExpectedClass: class, Args: args,
		CatalogHash: control.CatalogHash(entry.GetHarness().GetName()),
	})
}

// styledScreens reads styled pane screens for wait_ready: escapes kept,
// so the placeholder and trust checks read what the probe needs.
type styledScreens struct {
	obs *inventory.Observer
}

func (s *styledScreens) Capture(ctx context.Context, server, pane string) (string, error) {
	text, _, err := s.obs.CapturePane(ctx, server, pane, 0, true, harness.MaxScreenBytes)
	return text, err
}

// pipeCapture starts pipe-pane capture of the goal pane into the
// agent's raw files (K1): <state>/raw/goal-<id>.log, plain appends.
type pipeCapture struct {
	g *goalsRun
}

func (c *pipeCapture) ensure() *goalEnsurers {
	return c.g.ensurers()
}

func (c *pipeCapture) Start(ctx context.Context, pane *typev1.PaneKey, goal string) error {
	ge := c.ensure()
	e := ge.forPane(pane.GetServer())
	if e == nil {
		return fmt.Errorf("startgoal: no control client serves that tmux server")
	}
	path, err := rawFile(c.g.stateDir(), goal)
	if err != nil {
		return err
	}
	return e.StartPipeCapture(ctx, pane.GetPaneId(), path)
}

// rawFile ensures the K1 raw file exists before tmux opens it: pipe-pane
// runs `cat >>`, which would otherwise create the file under the process
// umask. Everything the agent creates is 0700 folders and 0600 files
// (01d2), and a second open appends, never truncates.
func rawFile(stateDir, goal string) (string, error) {
	dir := filepath.Join(stateDir, "raw")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("startgoal: mkdir raw: %w", err)
	}
	path := filepath.Join(dir, "goal-"+goal+".log")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600) //nolint:gosec // G304: the agent's own raw dir plus a validated goal id
	if err != nil {
		return "", fmt.Errorf("startgoal: create raw: %w", err)
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("startgoal: create raw: %w", err)
	}
	return path, nil
}

// diskFreeSource reports free bytes on the state volume (ok false when
// unreadable, so the start fails closed instead of guessing). The
// product saturates at math.MaxInt64 rather than wrapping, the way the
// hub's DiskFree does (internal/hub/system).
func diskFreeSource(stateDir func() string) func() (int64, bool) {
	return func() (int64, bool) {
		var st unix.Statfs_t
		if err := unix.Statfs(stateDir(), &st); err != nil {
			return 0, false
		}
		size := st.Bsize
		if size <= 0 {
			return 0, false
		}
		free := st.Bavail * uint64(size)
		if free > uint64(math.MaxInt64) {
			return math.MaxInt64, true
		}
		return int64(free), true
	}
}

// spoolPath is hook-events.jsonl: FLEET_HOOK_SPOOL wins, else the state
// dir default hook.py falls back to.
func spoolPath(stateDir string) string {
	if p := os.Getenv("FLEET_HOOK_SPOOL"); p != "" {
		return p
	}
	return filepath.Join(stateDir, "hook-events.jsonl")
}

// runSpool tails the hook spool into the registry (E8): hook lines
// become TurnEnd calls, which learn sessions and emit hook events.
func (g *goalsRun) runSpool(ctx context.Context) {
	if g == nil || g.reg == nil {
		return
	}
	tail, err := spool.New(spool.Config{
		Spool: spoolPath(g.stateDir()), Registry: g.reg.Registry(),
		Files: goalfile.Writer{EnsureBoard: ensureBoard},
	})
	if err != nil {
		g.log.Logger.ErrorContext(ctx, "spool.tail_failed", "err_kind", "io")
		return
	}
	if err := tail.Run(ctx); err != nil {
		g.log.Logger.ErrorContext(ctx, "spool.tail_stopped", "err_kind", "io")
	}
}
