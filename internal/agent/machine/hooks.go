package machine

import (
	"context"

	"google.golang.org/protobuf/proto"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
)

// Hooks adapts a [Collector] and a [Sampler] to the two hooks of the
// connection loop (conn.LoopOptions.Snapshot and .Heartbeat).
type Hooks struct {
	Collector *Collector
	Sampler   *Sampler
}

// Snapshot adds the B1 facts and the latest B2 sample to a Snapshot under
// construction. It keeps the facts the loop already set (hostname, boot_id,
// ...). Resources stay nil when there is no fresh sample: the hub then shows
// "unknown", not a stale or zero reading. It never fails the snapshot.
func (h Hooks) Snapshot(ctx context.Context, s *agentv1.Snapshot) error {
	if s.GetFacts() == nil {
		s.Facts = &agentv1.MachineFacts{}
	}
	h.Collector.Fill(ctx, s.GetFacts())
	if smp := h.Sampler.Fresh(); smp != nil {
		if r, ok := proto.Clone(smp.Resources).(*agentv1.ResourceSample); ok {
			s.Resources = r
		}
	}
	return nil
}

// Heartbeat sets load1 from the latest fresh sample and leaves it unset
// otherwise. It reads only what the Sampler already holds, as the loop
// requires of a Heartbeat hook (it runs on the sending goroutine).
func (h Hooks) Heartbeat(_ context.Context, hb *agentv1.Heartbeat) {
	if smp := h.Sampler.Fresh(); smp != nil && smp.Resources.Load1 != nil {
		hb.Load1 = proto.Float64(smp.Resources.GetLoad1())
	}
}
