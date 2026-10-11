package main

import (
	"context"
	"sync"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/events"
	"github.com/linuxdynasty/fleet-board/internal/agent/machine"
	"github.com/linuxdynasty/fleet-board/internal/proc"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

// healthSeams are the AgentHealth numbers the machine hooks read from other
// parts of the agent. A nil one reads 0 (machine.Hooks). Row 6e wires Refused
// and row 7 ControlDown.
type healthSeams struct {
	Unacked     func() int
	Reconnects  func() uint64
	Refused     func() uint64
	ControlDown func() uint32
}

// machineHooks starts the B2 sampler on wg (it stops with ctx) and returns
// the B1 facts and B2 sample as the connection loop's Snapshot and Heartbeat
// hooks. roots is the allowed repo roots now (nil until the policy exists,
// row 6b); the state folder is always sampled. With an emitter each sample
// is also sent as a resource_sample event (SHED, plan row 7).
func machineHooks(ctx context.Context, wg *sync.WaitGroup, tel *telemetry.Telemetry, dir string, roots func() []string, seams healthSeams, em *events.Emitter) (
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
	if em != nil {
		samples, unsubscribe := sampler.Subscribe()
		wg.Go(func() {
			defer unsubscribe()
			events.ResourceSamples(ctx, em, samples)
		})
	}
	h := machine.Hooks{
		Collector: collector, Sampler: sampler,
		Unacked: seams.Unacked, Reconnects: seams.Reconnects, Refused: seams.Refused, ControlDown: seams.ControlDown,
	}
	return h.Snapshot, h.Heartbeat, nil
}
