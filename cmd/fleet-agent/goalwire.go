package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/events"
	"github.com/linuxdynasty/fleet-board/internal/agent/goals"
	"github.com/linuxdynasty/fleet-board/internal/agent/repos"
	"github.com/linuxdynasty/fleet-board/internal/agent/ring"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux/inventory"
	"github.com/linuxdynasty/fleet-board/internal/proc"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

// goalsRun is the goals-and-repos observer of one run (row 9: E9, G2,
// H1, H3, H4), or nil without tmux: with no panes there is nothing to
// attribute and the Snapshot carries no goals or repos. The refresher
// rebuilds the checkout table (git) and forge state (gh); the tracker
// attributes goal files and emits GoalState events. One mutex guards both
// across the tick goroutine and the snapshot hook.
type goalsRun struct {
	mu        sync.Mutex
	refresher *repos.Refresher
	tracker   *goals.Tracker
	roots     func() []string
	log       *telemetry.Telemetry
}

// startGoals builds the refresher and the tracker. Roots come from the
// policy on every tick; nil without tmux.
func startGoals(tel *telemetry.Telemetry, machineID string, db *ring.DB, em *events.Emitter,
	tmx tmuxRun, roots func() []string,
) (*goalsRun, error) {
	if tmx.obs == nil {
		return nil, nil
	}
	runner, err := proc.NewRunner(tel)
	if err != nil {
		return nil, err
	}
	gitCaller, err := proc.For("git")
	if err != nil {
		return nil, err
	}
	ghCaller, err := proc.For("gh")
	if err != nil {
		return nil, err
	}
	refresher := repos.NewRefresher(repos.Config{
		Roots: roots(), Git: repos.NewGit(runner, gitCaller), Forge: repos.NewForge(runner, ghCaller),
	})
	return &goalsRun{
		refresher: refresher,
		tracker: goals.NewTracker(goals.Config{
			MachineID: machineID, Panes: &observerPanes{obs: tmx.obs}, Checkouts: refresher,
			Procs: goals.GopsutilProcs{}, Files: goals.OSFiles{}, DB: db, Emit: em,
			Captures: &observerCaptures{obs: tmx.obs},
		}),
		roots: roots,
		log:   tel,
	}, nil
}

// observerCaptures reads goal panes' screens for the condition observer
// (E12): the whole visible screen, ANSI stripped, through the inventory
// observer's read path. A capture that fails keeps the last condition
// level; it never looks like a recovery.
type observerCaptures struct {
	obs *inventory.Observer
}

func (c *observerCaptures) Capture(ctx context.Context, server, pane string) (string, error) {
	ctx, stop := context.WithTimeout(ctx, 10*time.Second)
	defer stop()
	text, _, err := c.obs.CapturePane(ctx, server, pane, 0, false, 0)
	return text, err
}

// run ticks every rescan: the table is rebuilt and the goals rescanned. A
// tick that fails is logged, never fatal: the next tick retries and the
// last readings stay. Goal panes' screens are re-read for harness
// conditions (E12) on the faster condition tick, so an interrupt at the
// machine reaches triage within seconds.
func (g *goalsRun) run(ctx context.Context) {
	if g == nil {
		return
	}
	log := g.log.Logger.With("component", "goals")
	tick := time.NewTicker(goals.RescanInterval())
	defer tick.Stop()
	condTick := time.NewTicker(goals.RescanConditionsInterval())
	defer condTick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			g.mu.Lock()
			g.refresher.SetRoots(g.roots())
			snap := &agentv1.Snapshot{}
			refreshErr := g.refresher.Refresh(ctx, snap)
			scanErr := g.tracker.Scan(ctx)
			g.mu.Unlock()
			if refreshErr != nil {
				log.WarnContext(ctx, "goals.refresh_failed", "err_kind", "io")
			}
			if scanErr != nil {
				log.WarnContext(ctx, "goals.scan_failed", "err_kind", "io")
			}
		case <-condTick.C:
			g.mu.Lock()
			scanErr := g.tracker.ScanConditions(ctx)
			g.mu.Unlock()
			if scanErr != nil {
				log.WarnContext(ctx, "goals.conditions_scan_failed", "err_kind", "io")
			}
		}
	}
}

// snapshot is the goals Snapshot hook: the table is rebuilt, the tracker
// scans once before the first fill (so the first snapshot already carries
// the goals), then fills the repos, the pane workdirs and the goals. Open
// harness conditions (E12) are primed and filled the same way, so triage
// is rebuilt at once after a reconnect.
func (g *goalsRun) snapshot(ctx context.Context, s *agentv1.Snapshot) error {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.refresher.SetRoots(g.roots())
	if err := g.refresher.Refresh(ctx, s); err != nil {
		return err
	}
	if !g.tracker.Primed() {
		if err := g.tracker.Scan(ctx); err != nil {
			return err
		}
	}
	g.tracker.Fill(s)
	if !g.tracker.ConditionsPrimed() {
		if err := g.tracker.ScanConditions(ctx); err != nil {
			return err
		}
	}
	g.tracker.FillConditions(s)
	return nil
}

// ambiguous is status.json's ambiguous_folders: the checkouts holding two
// harness panes.
func (g *goalsRun) ambiguous() []string {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.tracker.Ambiguous()
}

// observerPanes lists the observer's panes and locates them through the
// control clients (goals.Panes).
type observerPanes struct {
	obs *inventory.Observer
}

func (p *observerPanes) List(ctx context.Context) ([]goals.Pane, error) {
	snap := &agentv1.Snapshot{}
	if err := p.obs.Snapshot(ctx, snap); err != nil {
		return nil, err
	}
	var out []goals.Pane
	for _, srv := range snap.GetTmuxServers() {
		for _, sess := range srv.GetSessions() {
			if sess.GetInternal() {
				continue
			}
			for _, w := range sess.GetWindows() {
				for _, pane := range w.GetPanes() {
					key := pane.GetKey()
					out = append(out, goals.Pane{
						Key: key, Session: sess.GetSessionId(),
						Server: key.GetServer(), ID: key.GetPaneId(), Dead: pane.GetDead(),
					})
				}
			}
		}
	}
	return out, nil
}

func (p *observerPanes) Locate(ctx context.Context, server, id string) (string, int32, error) {
	c := p.obs.Client(server)
	if c == nil {
		return "", 0, fmt.Errorf("goals: no client for server %q", server)
	}
	n, err := strconv.Atoi(strings.TrimPrefix(id, "%"))
	if err != nil || n < 0 {
		return "", 0, fmt.Errorf("goals: bad pane id %q", id)
	}
	ctx, stop := context.WithTimeout(ctx, 10*time.Second)
	defer stop()
	at, err := c.PaneLocation(ctx, tmux.PaneID(n))
	if err != nil {
		return "", 0, err
	}
	return at.Path, at.PID, nil
}
