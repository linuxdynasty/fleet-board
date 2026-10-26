package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/exec"
	"github.com/linuxdynasty/fleet-board/internal/agent/goalfile"
	"github.com/linuxdynasty/fleet-board/internal/agent/preflight"
	"github.com/linuxdynasty/fleet-board/internal/agent/probe"
	"github.com/linuxdynasty/fleet-board/internal/agent/startgoal"
	"github.com/linuxdynasty/fleet-board/internal/harness"
)

// This file holds the row-21 StartGoal pane facts: the examiner the
// pick_session table reads, the RegisterGoal checkout seam, and the
// production InUse for PruneWorktree. The command surface (branches,
// windows, typing, capture, the spool tailer) lives in startgoalexec.go.

// defaultGoalCap is the 03e max_total default: the local goal cap when
// no settings layer names another.
const defaultGoalCap = 4

// setGoalCap installs the local goal cap after the settings manager
// opens: the merged max_total, or the 03e default.
func (g *goalsRun) setGoalCap(maxTotal func() uint32) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.maxGoals = func() int {
		if v := maxTotal(); v >= 1 {
			return int(v)
		}
		return defaultGoalCap
	}
}

// startgoalHandler returns the G3 StartGoal handler, or nil without
// tmux (no panes to start goals in).
func (g *goalsRun) startgoalHandler() exec.Handler {
	if g == nil || g.reg == nil || g.boardMgr == nil || g.tmx.obs == nil {
		return nil
	}
	deps := startgoal.Deps{
		Panes:    &paneExaminer{g: g},
		Git:      &gitSwitch{runner: g.runner, caller: g.gitCaller},
		Trees:    &managerTrees{mgr: g.boardMgr, stateDir: g.stateDir},
		Windows:  &goalWindows{g: g},
		Registry: g.reg.Registry(),
		Files:    goalfile.Writer{EnsureBoard: ensureBoard},
		Typer:    &goalTyper{g: g},
		Screens:  &styledScreens{obs: g.tmx.obs},
		Capture:  &pipeCapture{g: g},
		MaxGoals: g.maxGoalsOrDefault,
		DiskFree: diskFreeSource(g.stateDir),
		Now:      time.Now,
	}
	h, err := startgoal.NewHandler(deps)
	if err != nil {
		g.log.Logger.ErrorContext(context.Background(), "startgoal.handler_failed")
		return nil
	}
	return h
}

func (g *goalsRun) maxGoalsOrDefault() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.maxGoals == nil {
		return defaultGoalCap
	}
	return g.maxGoals()
}

// paneExaminer answers the StartGoal pane checks from the observer
// snapshot (the level facts), the checkout table, and one styled screen
// read for the probe classifier.
type paneExaminer struct {
	g *goalsRun
}

func (p *paneExaminer) snapshot(ctx context.Context) (*agentv1.Snapshot, error) {
	snap := &agentv1.Snapshot{}
	if err := p.g.tmx.obs.Snapshot(ctx, snap); err != nil {
		return nil, err
	}
	return snap, nil
}

func findPane(snap *agentv1.Snapshot, want *typev1.PaneKey) (*agentv1.TmuxPane, *agentv1.TmuxSession) {
	for _, srv := range snap.GetTmuxServers() {
		for _, sess := range srv.GetSessions() {
			for _, w := range sess.GetWindows() {
				for _, pane := range w.GetPanes() {
					k := pane.GetKey()
					if k.GetServer() == want.GetServer() &&
						k.GetServerStartId() == want.GetServerStartId() &&
						k.GetPaneId() == want.GetPaneId() {
						return pane, sess
					}
				}
			}
		}
	}
	return nil, nil
}

// Examine fills one pane's pick_session facts (02h). A pane the
// snapshot lost, or a dead one, reads as gone: reuse refuses
// SESSION_NOT_AVAILABLE instead of guessing.
func (p *paneExaminer) Examine(ctx context.Context, pane *typev1.PaneKey) (startgoal.PaneState, error) {
	p.g.mu.Lock()
	defer p.g.mu.Unlock()
	snap, err := p.snapshot(ctx)
	if err != nil {
		return startgoal.PaneState{}, err
	}
	found, sess := findPane(snap, pane)
	if found == nil || found.GetDead() {
		return startgoal.PaneState{Exists: false}, nil
	}
	ps := startgoal.PaneState{
		Exists: true, Harness: found.GetHarness().GetName(),
		FleetSession: found.GetStartedByFleet(),
		FreshMark:    found.GetAvailability().GetFreshContextOnReuse(),
		Session:      sess,
	}
	if e, ok := p.g.reg.Registry().ByPane(pane); ok {
		ps.BoundGoal = e.GetGoal().GetValue()
	}
	if dir := found.GetWorkdir().GetCurrentPath(); dir != "" {
		ps.PaneDir = dir
		if wd := p.g.refresher.Workdir(dir); wd != nil {
			ps.CheckoutTop = wd.GetCheckoutPath()
			ps.Dirty = wd.GetDirty()
		}
		for _, e := range p.g.reg.Registry().Entries() {
			if e.GetPane().String() == pane.String() {
				continue
			}
			if e.GetWorktreePath() != "" && e.GetWorktreePath() == ps.CheckoutTop {
				ps.CheckoutHeld = true
				break
			}
		}
	}
	screen, _, err := p.g.tmx.obs.CapturePane(ctx, pane.GetServer(), pane.GetPaneId(), 0, true, harness.MaxScreenBytes)
	if err == nil {
		switch c := probe.Classify(ps.Harness, screen); {
		case c.Outcome == probe.OutcomeTrust:
			ps.TrustBox = true
		case c.Outcome == probe.OutcomeStartup:
			ps.StartupKind = c.Kind
			if ps.StartupKind == "" {
				ps.StartupKind = "startup"
			}
		case c.Outcome == probe.OutcomeReady && c.Input == probe.InputEmpty:
			ps.Ready, ps.InputEmpty = true, true
		case c.Input == probe.InputDraft:
			ps.InputDraft = true
		}
	}
	return ps, nil
}

// LookupRepo resolves a RepoRef through the checkout table under the
// run mutex.
func (p *paneExaminer) LookupRepo(repo *typev1.RepoRef) (string, error) {
	p.g.mu.Lock()
	defer p.g.mu.Unlock()
	if top, ok := p.g.refresher.LookupRepo(repo.GetHost(), repo.GetPath()); ok {
		return top, nil
	}
	return "", fmt.Errorf("startgoal: no local checkout of %s/%s", repo.GetHost(), repo.GetPath())
}

// PushAccess asks gh for the checkout's push access (H4): unknown when
// gh cannot answer, so the hub's fresher reading rules.
func (p *paneExaminer) PushAccess(ctx context.Context, repo *typev1.RepoRef) (bool, bool) {
	top, err := p.LookupRepo(repo)
	if err != nil || top == "" {
		return false, false
	}
	a, ok := p.g.forge.Access(ctx, top)
	if !ok {
		return false, false
	}
	return a.Push, true
}

// paneInfo derives the checkout and harness for RegisterGoal from the
// snapshot (no hub path, 02h): pane checkout top plus the detected
// harness. Absent facts refuse instead of guessing.
func (g *goalsRun) paneInfo(pane *typev1.PaneKey) (string, string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	snap := &agentv1.Snapshot{}
	ctx, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	if err := g.tmx.obs.Snapshot(ctx, snap); err != nil {
		return "", "", err
	}
	found, _ := findPane(snap, pane)
	if found == nil || found.GetDead() {
		return "", "", fmt.Errorf("goals: the pane is gone")
	}
	wd := found.GetWorkdir()
	if wd == nil || wd.GetCheckoutPath() == "" {
		return "", "", fmt.Errorf("goals: the pane works in no checkout")
	}
	return wd.GetCheckoutPath(), found.GetHarness().GetName(), nil
}

// livePanePaths lists the working directories of live panes: the
// production InUse answers whether a goal worktree still holds a pane.
func (g *goalsRun) livePanePaths(ctx context.Context) map[string]bool {
	out := map[string]bool{}
	snap := &agentv1.Snapshot{}
	if err := g.tmx.obs.Snapshot(ctx, snap); err != nil {
		return out
	}
	for _, srv := range snap.GetTmuxServers() {
		for _, sess := range srv.GetSessions() {
			if sess.GetInternal() {
				continue
			}
			for _, w := range sess.GetWindows() {
				for _, pane := range w.GetPanes() {
					if pane.GetDead() {
						continue
					}
					if dir := pane.GetWorkdir().GetCurrentPath(); dir != "" {
						out[dir] = true
					}
				}
			}
		}
	}
	return out
}

// inUse reports whether a live pane works at or under path (the
// worktree Manager's production seam): pruning waits while one does.
func (g *goalsRun) inUse(path string) bool {
	ctx, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	for dir := range g.livePanePaths(ctx) {
		if dir == path || strings.HasPrefix(dir, path+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// preflightHandler returns the I1 Preflight handler, or nil without
// tmux (no panes or checkouts to judge).
func (g *goalsRun) preflightHandler() exec.Handler {
	if g == nil || g.tmx.obs == nil {
		return nil
	}
	return preflight.Handler{Checker: &preflight.Checker{
		Reader: preflightReader{
			panes:    &paneExaminer{g: g},
			diskFree: diskFreeSource(g.stateDir),
		},
		Typer: neverTyper{},
	}}
}

// preflightReader answers the I1 checks from production seams: the
// checkout table and forge access through the pane examiner, the free
// space on the state volume. Sign-in stays unknown (no agent-side
// source before a harness screen is read; the checker skips it) as
// does folder trust (nothing is open to prompt pre-pane; the StartGoal
// reuse path re-checks the live pane).
type preflightReader struct {
	panes    startgoal.Panes
	diskFree func() (int64, bool)
}

func (r preflightReader) Facts(ctx context.Context, spec *agentv1.GoalSpec) (preflight.Facts, error) {
	if spec == nil {
		return preflight.Facts{}, fmt.Errorf("preflight: nil goal spec")
	}
	repo := spec.GetRepo()
	top, err := r.panes.LookupRepo(repo)
	push, known := r.panes.PushAccess(ctx, repo)
	free, ok := r.diskFree()
	var bytes uint64
	if ok && free > 0 {
		bytes = uint64(free)
	}
	return preflight.Facts{
		CheckoutFound:   err == nil && top != "",
		PushKnown:       known,
		PushAccess:      push,
		FolderUntrusted: false,
		DiskKnown:       ok,
		DiskFreeBytes:   bytes,
	}, nil
}

// neverTyper refuses every keystroke: preflight only reads, and Check
// never calls it. A call would be a bug, so it fails loudly.
type neverTyper struct{}

func (neverTyper) TypeKeys(context.Context, string) error {
	return fmt.Errorf("preflight never types")
}
