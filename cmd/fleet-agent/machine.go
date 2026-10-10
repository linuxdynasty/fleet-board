package main

import (
	"context"
	"sync"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/machine"
	"github.com/linuxdynasty/fleet-board/internal/proc"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

// machineHooks starts the B2 sampler on wg (it stops with ctx) and returns
// the B1 facts and B2 sample as the connection loop's Snapshot and Heartbeat
// hooks. roots is the allowed repo roots now (nil until the policy exists,
// row 6b); the state folder is always sampled.
func machineHooks(ctx context.Context, wg *sync.WaitGroup, tel *telemetry.Telemetry, dir string, roots func() []string) (
	snapshot func(context.Context, *agentv1.Snapshot) error, heartbeat func(context.Context, *agentv1.Heartbeat), err error,
) {
	m, err := machine.NewMetrics(tel)
	if err != nil {
		return nil, nil, err
	}
	runner, err := proc.NewRunner(tel)
	if err != nil {
		return nil, nil, err
	}
	sampler, err := machine.NewSampler(machine.SamplerOptions{StateDir: dir, Roots: roots, Metrics: m})
	if err != nil {
		return nil, nil, err
	}
	collector, err := machine.NewCollector(ctx, machine.CollectorOptions{Runner: runner, Metrics: m})
	if err != nil {
		return nil, nil, err
	}
	wg.Go(func() { sampler.Run(ctx) })
	h := machine.Hooks{Collector: collector, Sampler: sampler}
	return h.Snapshot, h.Heartbeat, nil
}
