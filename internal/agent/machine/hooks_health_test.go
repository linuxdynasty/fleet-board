package machine_test

import (
	"context"
	"math"
	"testing"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/machine"
)

// The heartbeat's AgentHealth (05b section 6) is filled from what the agent
// already holds: no query, no wait. Every number here comes from a seam.
func TestHeartbeatHookFillsAgentHealth(t *testing.T) {
	t.Parallel()
	s := one(t, machine.SamplerOptions{StateDir: "/state", Readers: good()})
	h := hooksFor(t, s)
	h.Unacked = func() int { return 3 }
	h.Reconnects = func() uint64 { return 7 }
	h.Refused = func() uint64 { return 11 }
	h.ControlDown = func() uint32 { return 2 }
	hb := &agentv1.Heartbeat{}
	h.Heartbeat(context.Background(), hb)
	got := hb.GetHealth()
	if got == nil {
		t.Fatal("no health in the heartbeat")
	}
	if got.GetUnackedResults() != 3 || got.GetReconnectsTotal() != 7 || got.GetCommandsRefusedTotal() != 11 || got.GetTmuxControlDown() != 2 {
		t.Errorf("health = %v, want unacked 3, reconnects 7, refused 11, control down 2", got)
	}
	if got.RssBytes == nil || got.GetRssBytes() != 96<<20 {
		t.Errorf("rss_bytes = %v, want 96 MiB from the sample", got.RssBytes)
	}
	// The hook leaves the ring fields to the connection loop, which sets them
	// from the event ring after this hook runs, so they stay 0 here.
	if got.GetRingEvents() != 0 || got.GetRingBytes() != 0 || got.GetProbesFailing() != 0 {
		t.Errorf("health = %v, want ring_events, ring_bytes and probes_failing 0", got)
	}
}

// A seam that is not wired means 0, which is true while no executor or
// tmux control connection exists; the health message is still sent.
func TestHeartbeatHealthSeamsDefaultToZero(t *testing.T) {
	t.Parallel()
	s := one(t, machine.SamplerOptions{StateDir: "/state", Readers: good()})
	hb := &agentv1.Heartbeat{}
	hooksFor(t, s).Heartbeat(context.Background(), hb)
	got := hb.GetHealth()
	if got == nil || got.GetUnackedResults() != 0 || got.GetReconnectsTotal() != 0 || got.GetCommandsRefusedTotal() != 0 || got.GetTmuxControlDown() != 0 {
		t.Fatalf("health = %v, want a message of zeros", got)
	}
}

// An unreadable RSS leaves the field unset (unknown), as does a sample too
// old to vouch for; the counters still go out.
func TestHeartbeatHealthLeavesRSSUnsetWhenItIsUnknown(t *testing.T) {
	t.Parallel()
	rd := good()
	rd.RSS = func(context.Context) (uint64, error) { return 0, nil }
	s := one(t, machine.SamplerOptions{StateDir: "/state", Readers: rd})
	h := hooksFor(t, s)
	h.Reconnects = func() uint64 { return 4 }
	hb := &agentv1.Heartbeat{}
	h.Heartbeat(context.Background(), hb)
	if hb.GetHealth().RssBytes != nil {
		t.Errorf("rss_bytes = %d for an RSS of 0, want unset", hb.GetHealth().GetRssBytes())
	}
	if hb.GetHealth().GetReconnectsTotal() != 4 {
		t.Errorf("reconnects_total = %d, want 4", hb.GetHealth().GetReconnectsTotal())
	}

	c := newClock()
	old := one(t, machine.SamplerOptions{StateDir: "/state", Readers: good(), Now: c.now})
	c.add(time.Hour)
	h2 := hooksFor(t, old)
	hb = &agentv1.Heartbeat{}
	h2.Heartbeat(context.Background(), hb)
	if hb.GetHealth() == nil || hb.GetHealth().RssBytes != nil {
		t.Errorf("health = %v from a sample an hour old, want health with no rss_bytes", hb.GetHealth())
	}
	// No sample at all (the sampler has not run): the same.
	k := newTicker()
	never, err := machine.NewSampler(machine.SamplerOptions{StateDir: "/state", Readers: good(), Ticks: k.fn})
	if err != nil {
		t.Fatal(err)
	}
	hb = &agentv1.Heartbeat{}
	hooksFor(t, never).Heartbeat(context.Background(), hb)
	if hb.GetHealth() == nil || hb.GetHealth().RssBytes != nil {
		t.Errorf("health = %v before any sample, want health with no rss_bytes", hb.GetHealth())
	}
}

// unacked_results is a uint32 on the wire: a count past it saturates, it does
// not wrap to a small number.
func TestHeartbeatHealthSaturatesTheUnackedCount(t *testing.T) {
	t.Parallel()
	h := hooksFor(t, one(t, machine.SamplerOptions{StateDir: "/state", Readers: good()}))
	h.Unacked = func() int { return math.MaxUint32 + 5 }
	hb := &agentv1.Heartbeat{}
	h.Heartbeat(context.Background(), hb)
	if got := hb.GetHealth().GetUnackedResults(); got != math.MaxUint32 {
		t.Fatalf("unacked_results = %d, want %d", got, uint32(math.MaxUint32))
	}
	h.Unacked = func() int { return -1 }
	hb = &agentv1.Heartbeat{}
	h.Heartbeat(context.Background(), hb)
	if got := hb.GetHealth().GetUnackedResults(); got != 0 {
		t.Fatalf("unacked_results = %d for a negative count, want 0", got)
	}
}
