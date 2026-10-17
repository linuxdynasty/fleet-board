package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"github.com/linuxdynasty/fleet-board/internal/agent/exec"
	"github.com/linuxdynasty/fleet-board/internal/agent/probe"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux/inventory"
	"github.com/linuxdynasty/fleet-board/internal/proc"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

// probeRun is the usage-probe manager of one run (01 D1, 03b), or nil
// without tmux: with no tmux server there is nowhere to keep an idle
// harness session. The manager owns one idle harness session per assigned
// account on the fleet-probe server, in the fleet-owned probe folder. It
// answers EnsureProbe and StopProbe through handlers (below); the command
// path registers them in a later row, so for now they only prove the
// manager builds the commands it will serve.
type probeRun struct{ mgr *probe.Manager }

// handlers are the EnsureProbe and StopProbe commands for the executor's
// registry: none without a manager.
func (p *probeRun) handlers() []exec.Handler {
	if p == nil || p.mgr == nil {
		return nil
	}
	return []exec.Handler{p.mgr.EnsureProbeHandler(), p.mgr.StopProbeHandler()}
}

// startProbe builds the probe manager and runs it on wg until ctx ends.
// Without tmux there is no probe server, so it returns nil and the agent
// runs on with no usage probes. The emitter is the interface the manager
// reports on; run passes its event emitter.
func startProbe(ctx context.Context, wg *sync.WaitGroup, tel *telemetry.Telemetry, em probe.Emitter, tmx tmuxRun) (*probeRun, error) {
	if tmx.obs == nil {
		return nil, nil
	}
	dir, err := defaultProbeDir()
	if err != nil {
		return nil, err
	}
	mgr, err := buildProbeManager(tel, em, tmx.program, probeSocket(), dir)
	if err != nil {
		return nil, err
	}
	wg.Go(func() { _ = mgr.Run(ctx) })
	return &probeRun{mgr: mgr}, nil
}

// probeSocket is the fleet-probe server's socket: the same folder the tmux
// observer watches, so the backend and the observer meet on one server.
func probeSocket() string {
	return filepath.Join(inventory.SocketFolder(os.Getenv, os.Getuid()), "fleet-probe")
}

// buildProbeManager checks the probe setup and fails fast: the tmux program
// for the fleet-probe server, its socket (there is no default server), the
// fleet-owned probe folder every session starts in, and the emitter for
// ProbeHealth and UsageReading events. Harness binaries are resolved on PATH
// with LookPath, which never starts a process to find one.
func buildProbeManager(tel *telemetry.Telemetry, em probe.Emitter, tmuxPath, socket, dir string) (*probe.Manager, error) {
	if tmuxPath == "" {
		return nil, errors.New("probe: tmux program is required")
	}
	runner, err := proc.NewRunner(tel)
	if err != nil {
		return nil, err
	}
	be, err := probe.NewTMux(runner, tmuxPath, socket)
	if err != nil {
		return nil, err
	}
	return probe.NewManager(probe.Options{
		Backend: be, Events: em, ProbeDir: dir, LookPath: proc.LookPath,
	})
}
