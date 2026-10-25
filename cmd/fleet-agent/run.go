package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sync"

	"github.com/spf13/cobra"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
	"github.com/linuxdynasty/fleet-board/internal/agent/events"
	"github.com/linuxdynasty/fleet-board/internal/agent/exec"
	"github.com/linuxdynasty/fleet-board/internal/agent/policy"
	"github.com/linuxdynasty/fleet-board/internal/agent/ring"
	"github.com/linuxdynasty/fleet-board/internal/agent/settings"
	"github.com/linuxdynasty/fleet-board/internal/agent/wakenet"
	"github.com/linuxdynasty/fleet-board/internal/hub/lock"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

// lockFile keeps a second `run` off the same state folder (#92): two would
// share agent.db's single writer and race on the event seqs.
const lockFile = "fleet-agent.lock"

// lock.ErrHeld names fleetd, so the agent says it in its own words.
var errAnotherRun = errors.New("another fleet-agent run holds the lock")

// runCommand is `fleet-agent run`: stay connected to the hub until a signal
// (exit 0) or until the hub or the certificate ends the loop (exit 1). A run
// that finds the hub's end in ended.json exits 1 before any network call.
func runCommand(dir, cfg func() (string, error), version string, discover conn.Discover, stderr io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   "run",
		Short: "Connect this machine to its hub and stay connected",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			d, err := dir()
			if err != nil {
				return commandError{err}
			}
			cd, err := cfg()
			if err != nil {
				return commandError{err}
			}
			boot := bootID()
			tel, err := telemetry.New(telemetry.Config{
				Service: telemetry.Agent, Version: version, InstanceID: boot, LogOutput: stderr,
			})
			if err != nil {
				return commandError{err}
			}
			defer func() { _ = tel.Shutdown(context.WithoutCancel(cmd.Context())) }()
			return runAgent(cmd.Context(), d, cd, version, boot, tel, discover)
		},
	}
}

func runAgent(ctx context.Context, dir, cfgDir, version, boot string, tel *telemetry.Telemetry, discover conn.Discover) error {
	return runAgentWithTmux(ctx, dir, cfgDir, version, boot, tel, discover, startTmux)
}

type agentRunDeps struct {
	tmuxStart        func(context.Context, *sync.WaitGroup, *telemetry.Telemetry) (tmuxRun, error)
	onWired          []func(*settings.Manager)
	newPolicyWatcher func() (policy.Watcher, error)
}

// runAgentWithTmux keeps the production run path while tests place its real
// observer on an isolated tmux socket rather than the developer's server.
func runAgentWithTmux(ctx context.Context, dir, cfgDir, version, boot string, tel *telemetry.Telemetry,
	discover conn.Discover, tmuxStart func(context.Context, *sync.WaitGroup, *telemetry.Telemetry) (tmuxRun, error),
	onWired ...func(*settings.Manager),
) error {
	return runAgentWithDeps(ctx, dir, cfgDir, version, boot, tel, discover, agentRunDeps{
		tmuxStart: tmuxStart, onWired: onWired,
	})
}

func runAgentWithDeps(ctx context.Context, dir, cfgDir, version, boot string, tel *telemetry.Telemetry,
	discover conn.Discover, deps agentRunDeps,
) error {
	log := tel.Logger.With("component", "run")
	failed := func(step string, err error) error {
		// A signal during startup is a clean stop, not a failure, but only
		// when the signal is what failed the step: a real failure (a loose
		// file, not enrolled) that coincides with one is still a failure.
		if cerr := ctx.Err(); cerr != nil && errors.Is(err, cerr) {
			log.InfoContext(ctx, "run.stopped", "step", step)
			return nil
		}
		log.ErrorContext(ctx, "run.failed", "step", step, "err_kind", runErrKind(err))
		return commandError{err}
	}
	id, err := conn.LoadIdentity(dir)
	if err != nil {
		return failed("identity", err)
	}
	// From here every line names the machine.
	log = log.With("machine_id", id.MachineID)
	path := filepath.Join(dir, lockFile)
	lk, err := lock.Acquire(path)
	if errors.Is(err, lock.ErrHeld) {
		return failed("lock", fmt.Errorf("%w: %s; only one may run per state folder", errAnotherRun, path))
	}
	if err != nil {
		return failed("lock", err)
	}
	defer func() { _ = lk.Release() }()
	// Before any network use: the end an earlier run stopped on (03 W1).
	removed, err := conn.CheckEnded(dir, id)
	var ended *conn.EndedError
	switch {
	case errors.As(err, &ended):
		log.ErrorContext(ctx, "run.refused", "end", string(ended.End))
		return commandError{err}
	case err != nil:
		return failed("ended", err)
	case removed:
		log.InfoContext(ctx, "run.ended_removed", "file", conn.EndedFile)
	}
	db, err := ring.Open(ctx, dir, ring.Options{Telemetry: tel})
	if err != nil {
		return failed("store", err)
	}
	defer func() { _ = db.Close() }()
	// The emitter closes after the policy and the sampler stop (defers run
	// last first) and before agent.db.
	em, err := events.New(ctx, events.Options{DB: db, Telemetry: tel})
	if err != nil {
		return failed("events", err)
	}
	defer em.Close()

	wctx, stop := context.WithCancel(ctx)
	var wg sync.WaitGroup
	pol, err := startPolicyWithWatcher(wctx, cfgDir, dir, id.MachineID, tel, events.PolicyChanges(em), deps.newPolicyWatcher)
	if err != nil {
		stop()
		return failed("policy", err)
	}
	var sm *settings.Manager
	defer func() {
		stop()
		wg.Wait()
		if sm != nil {
			sm.Close()
		}
		pol.close()
	}()
	netEvents := make(chan wakenet.Event)
	wlog := tel.Logger.With("component", "wakenet")
	wake, nw := wakenet.NewWake(wlog), wakenet.NewNet(wlog)
	wg.Go(func() { wake.Run(wctx, netEvents) })
	wg.Go(func() { nw.Run(wctx, netEvents) })

	// The heartbeat hook reads the loop's reconnect count; the loop exists only
	// after the hook, and the hook runs only inside loop.Run.
	var loop *conn.Loop
	var ex *exec.Executor
	var tmx tmuxRun
	snapshot, heartbeat, err := machineHooks(wctx, &wg, tel, dir, nil, healthSeams{
		Unacked: db.UnackedCount, Reconnects: func() uint64 { return loop.Reconnects() },
		Refused:     func() uint64 { return ex.RefusedTotal() },
		ControlDown: func() uint32 { return tmx.controlDown() },
	}, em)
	if err != nil {
		return failed("machine", err)
	}
	tmx, err = deps.tmuxStart(wctx, &wg, tel)
	if err != nil {
		return failed("tmux", err)
	}
	// The goals-and-repos observer (row 9) reads the pane checkouts and
	// attributes the goal files: its GoalState events travel on the
	// stream, its repos and goals in every Snapshot.
	roots := func() []string { return pol.mgr.Policy().AllowedRepoRoots }
	observed, err := startGoals(tel, id.MachineID, db, em, tmx, roots)
	if err != nil {
		return failed("goals", err)
	}
	wg.Go(func() { observed.run(wctx) })
	// The usage-probe manager (01 D1): one idle harness session per
	// assigned account on the fleet-probe server. Its EnsureProbe and
	// StopProbe handlers register with the command path in a later row;
	// Snapshot.probes[] lands there too.
	if _, err := startProbe(wctx, &wg, tel, em, tmx); err != nil {
		return failed("probe", err)
	}
	loop, err = conn.NewLoop(ctx, conn.LoopOptions{
		Dir: dir, Identity: id, DB: db, Version: version, BootID: boot,
		Telemetry: tel, Events: netEvents, PushOff: nw.PushOff, Discover: discover,
		Snapshot: chainSnapshot(snapshot, pol.mgr.Snapshot, func(ctx context.Context, s *agentv1.Snapshot) error {
			return sm.Snapshot(ctx, s)
		}, tmx.snapshot, observed.snapshot), Heartbeat: heartbeat, Emitter: em,
		Command:      func(ctx context.Context, epoch uint64, c *agentv1.Command) bool { return ex.Command(ctx, epoch, c) },
		Settings:     func(ctx context.Context, epoch uint64, s *agentv1.Settings) bool { return sm.Hook(ctx, epoch, s) },
		OnConnected:  func(ctx context.Context, epoch uint64, clock conn.HubClock) { ex.OnConnected(ctx, epoch, clock) },
		Capabilities: func() []*typev1.Capability { return ex.Capabilities() },
	})
	if err != nil {
		return failed("loop", err)
	}
	sm, err = settings.Open(wctx, settings.Options{
		DB: db, Local: pol.mgr, Sender: loop, Telemetry: tel,
	})
	if err != nil {
		return failed("settings", err)
	}
	h, err := interruptHandler(tmx, sm, tel)
	if err != nil {
		return failed("interrupt", err)
	}
	var handlers []exec.Handler
	if h != nil {
		handlers = append(handlers, h)
	}
	// The goal registry (row 20 lane 3): RegisterGoal and ReleaseGoal
	// bind it; nil without tmux. Its file and pane-checkout seams land
	// with lane 1 and row 21.
	handlers = append(handlers, observed.goalHandlers()...)
	ex, err = exec.New(exec.Options{
		DB: db, Sender: loop, Clock: loop.HubClock(), Policy: sm,
		Audit: pol.audit, Handlers: handlers, Telemetry: tel,
		Logger: tel.Logger.With("component", "exec", "machine_id", id.MachineID),
		// L3: a wait for local approval reads the person's answer
		// from the state folder (`fleet-agent approve|deny`). With no
		// answer the wait ends at the deadline as LOCAL_CONFIRM_TIMEOUT.
		Approver: exec.FileApprover{Dir: dir},
	})
	if err != nil {
		return failed("executor", err)
	}
	wg.Go(func() { _ = sm.Run(wctx) })
	wg.Go(func() { _ = ex.Run(wctx) })
	for _, report := range deps.onWired {
		report(sm)
	}
	defer startStatusFile(dir, tel, loop, pol.mgr.WatchOff, tmx.watch, observed.ambiguous)()
	if err := loop.Run(ctx); err != nil {
		return commandError{err}
	}
	return nil
}

func runErrKind(err error) string {
	switch {
	case errors.Is(err, conn.ErrNotEnrolled), errors.Is(err, conn.ErrLooseMode),
		errors.Is(err, ring.ErrLooseMode), errors.Is(err, errAnotherRun):
		return "constraint"
	case errors.Is(err, conn.ErrEndedDamaged):
		return "parse"
	default:
		return "io"
	}
}
