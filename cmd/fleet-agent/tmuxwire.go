package main

import (
	"context"
	"os"
	"sync"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/machine"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux/inventory"
	"github.com/linuxdynasty/fleet-board/internal/proc"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

// tmuxRun is the tmux observer of one run (01 E1, E2, E3), or the zero
// value when no tmux is installed: then the Snapshot has no tmux_servers
// and status.json names no folder. Program is the tmux binary the observer
// uses; the probe backend reuses it for the fleet-probe server.
type tmuxRun struct {
	obs     *inventory.Observer
	program string
}

// startTmux finds tmux and runs the observer until ctx ends, on wg. Its
// lifecycle events go nowhere yet: the emitter is wired to the stream with
// row 7 PR 7a-B, so for now the inventory travels in each Snapshot.
func startTmux(ctx context.Context, wg *sync.WaitGroup, tel *telemetry.Telemetry) (tmuxRun, error) {
	program := machine.TmuxPath(os.Getenv("PATH"), machine.DefaultTmuxDirs)
	if program == "" {
		return tmuxRun{}, nil
	}
	runner, err := proc.NewRunner(tel)
	if err != nil {
		return tmuxRun{}, err
	}
	obs, err := inventory.New(inventory.Options{Program: program, Runner: runner, Telemetry: tel})
	if err != nil {
		return tmuxRun{}, err
	}
	wg.Go(func() { obs.Run(ctx) })
	return tmuxRun{obs: obs, program: program}, nil
}

// snapshot is the observer's Snapshot hook (nothing without tmux).
func (t tmuxRun) snapshot(ctx context.Context, s *agentv1.Snapshot) error {
	if t.obs == nil {
		return nil
	}
	return t.obs.Snapshot(ctx, s)
}

// watch is status.json's tmux_folder and tmux_watch.
func (t tmuxRun) watch() (string, bool) {
	if t.obs == nil {
		return "", false
	}
	return t.obs.Folder(), t.obs.WatchOff()
}

// controlDown reports every configured observer client, including the probe
// server, without exposing either server to command dispatch.
func (t tmuxRun) controlDown() uint32 {
	if t.obs == nil {
		return 0
	}
	clients := make([]*tmux.Client, 0, 3)
	for _, name := range []string{"default", "fleet", "fleet-probe"} {
		if client := t.obs.Client(name); client != nil {
			clients = append(clients, client)
		}
	}
	return tmux.ControlDown(clients...)
}
