package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sync"

	"github.com/spf13/cobra"

	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
	"github.com/linuxdynasty/fleet-board/internal/agent/ring"
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
func runCommand(dir func() (string, error), version string, stderr io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   "run",
		Short: "Connect this machine to its hub and stay connected",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			d, err := dir()
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
			return runAgent(cmd.Context(), d, version, boot, tel)
		},
	}
}

func runAgent(ctx context.Context, dir, version, boot string, tel *telemetry.Telemetry) error {
	log := tel.Logger.With("component", "run")
	failed := func(step string, err error) error {
		if ctx.Err() != nil {
			// A signal during startup is a clean stop, not a failure.
			log.InfoContext(ctx, "run.stopped", "step", step)
			return nil //nolint:nilerr // the error is the stop itself (a cancelled context), so exit 0
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

	wctx, stop := context.WithCancel(ctx)
	var wg sync.WaitGroup
	defer func() { stop(); wg.Wait() }()
	events := make(chan wakenet.Event)
	wlog := tel.Logger.With("component", "wakenet")
	wake, nw := wakenet.NewWake(wlog), wakenet.NewNet(wlog)
	wg.Go(func() { wake.Run(wctx, events) })
	wg.Go(func() { nw.Run(wctx, events) })

	loop, err := conn.NewLoop(ctx, conn.LoopOptions{
		Dir: dir, Identity: id, DB: db, Version: version, BootID: boot,
		Telemetry: tel, Events: events, PushOff: nw.PushOff,
	})
	if err != nil {
		return failed("loop", err)
	}
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
