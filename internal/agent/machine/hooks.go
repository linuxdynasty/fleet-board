package machine

import (
	"context"
	"math"

	"google.golang.org/protobuf/proto"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
)

// Hooks adapts a [Collector] and a [Sampler] to the two hooks of the
// connection loop (conn.LoopOptions.Snapshot and .Heartbeat).
type Hooks struct {
	Collector *Collector
	Sampler   *Sampler

	// The numbers of AgentHealth that other packages own, each a read from
	// memory (the Heartbeat hook runs on the sending goroutine and must not
	// wait). A nil seam reads 0, which is true while nothing produces it.
	// Unacked is ring.DB.UnackedCount, Reconnects is conn.Loop.Reconnects,
	// Refused is the executor's refusal count (row 6e) and ControlDown is the
	// number of tmux servers whose control connection is down (row 7).
	Unacked     func() int
	Reconnects  func() uint64
	Refused     func() uint64
	ControlDown func() uint32
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

// Heartbeat sets load1 and health.rss_bytes from the latest fresh sample and
// leaves them unset otherwise, and fills the rest of health from the seams.
// It reads only what the Sampler and the seams already hold, as the loop
// requires of a Heartbeat hook (it runs on the sending goroutine).
//
// ring_events and ring_bytes stay 0, which is true until row 7 holds an event
// ring; probes_failing is row 8's.
func (h Hooks) Heartbeat(_ context.Context, hb *agentv1.Heartbeat) {
	health := &agentv1.AgentHealth{}
	hb.Health = health
	if smp := h.Sampler.Fresh(); smp != nil {
		if smp.Resources.Load1 != nil {
			hb.Load1 = proto.Float64(smp.Resources.GetLoad1())
		}
		if smp.RSSBytes != 0 {
			health.RssBytes = proto.Uint64(smp.RSSBytes)
		}
	}
	if h.Unacked != nil {
		health.UnackedResults = uint32(min(max(h.Unacked(), 0), math.MaxUint32))
	}
	if h.Reconnects != nil {
		health.ReconnectsTotal = h.Reconnects()
	}
	if h.Refused != nil {
		health.CommandsRefusedTotal = h.Refused()
	}
	if h.ControlDown != nil {
		health.TmuxControlDown = h.ControlDown()
	}
}
