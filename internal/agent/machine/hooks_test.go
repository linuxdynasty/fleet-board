package machine_test

import (
	"context"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/machine"
)

func hooksFor(t *testing.T, s *machine.Sampler) machine.Hooks {
	t.Helper()
	c, err := machine.NewCollector(t.Context(), machine.CollectorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return machine.Hooks{Collector: c, Sampler: s}
}

func TestSnapshotHookAddsFactsAndResourcesAndKeepsTheLoopsFacts(t *testing.T) {
	t.Parallel()
	s := one(t, machine.SamplerOptions{StateDir: "/state", Readers: good()})
	snap := &agentv1.Snapshot{Seq: 7, Facts: &agentv1.MachineFacts{Hostname: "h", BootId: "boot-1", CpuCount: 8}}
	if err := hooksFor(t, s).Snapshot(t.Context(), snap); err != nil {
		t.Fatal(err)
	}
	if snap.GetFacts().GetHostname() != "h" || snap.GetFacts().GetBootId() != "boot-1" || snap.GetFacts().GetCpuCount() != 8 {
		t.Fatalf("the hook changed the loop's facts: %v", snap.GetFacts())
	}
	if snap.GetFacts().GetRamBytes() == 0 || snap.GetSeq() != 7 {
		t.Fatalf("facts or seq wrong: %v", snap)
	}
	if !proto.Equal(snap.GetResources(), s.Latest().Resources) {
		t.Fatalf("resources = %v, want the latest sample", snap.GetResources())
	}
	// The snapshot holds a copy: changing it must not change the sampler's.
	snap.Resources.Load1 = proto.Float64(99)
	if s.Latest().Resources.GetLoad1() == 99 {
		t.Fatal("the snapshot shares the sampler's message")
	}
	// A snapshot with no facts yet gets some.
	empty := &agentv1.Snapshot{}
	if err := hooksFor(t, s).Snapshot(t.Context(), empty); err != nil || empty.GetFacts() == nil {
		t.Fatalf("Snapshot on empty = %v, %v", empty, err)
	}
}

func TestSnapshotHookLeavesResourcesNilWithoutAFreshSample(t *testing.T) {
	t.Parallel()
	k := newTicker()
	never, err := machine.NewSampler(machine.SamplerOptions{StateDir: "/state", Readers: good(), Ticks: k.fn})
	if err != nil {
		t.Fatal(err)
	}
	snap := &agentv1.Snapshot{}
	if err := hooksFor(t, never).Snapshot(t.Context(), snap); err != nil || snap.GetResources() != nil {
		t.Fatalf("before any sample: resources = %v, %v; want nil", snap.GetResources(), err)
	}
	c := newClock()
	old := one(t, machine.SamplerOptions{StateDir: "/state", Readers: good(), Now: c.now})
	c.add(time.Hour)
	if err := hooksFor(t, old).Snapshot(t.Context(), snap); err != nil || snap.GetResources() != nil {
		t.Fatalf("a sample an hour old was reported as current: %v", snap.GetResources())
	}
}

func TestHeartbeatHookSetsLoad1OnlyFromAFreshSample(t *testing.T) {
	t.Parallel()
	c := newClock()
	s := one(t, machine.SamplerOptions{StateDir: "/state", Readers: good(), Now: c.now})
	h := hooksFor(t, s)
	hb := &agentv1.Heartbeat{}
	h.Heartbeat(context.Background(), hb)
	if hb.Load1 == nil || hb.GetLoad1() != 0.25 {
		t.Fatalf("load1 = %v, want 0.25", hb.Load1)
	}
	c.add(time.Hour)
	hb = &agentv1.Heartbeat{}
	h.Heartbeat(context.Background(), hb)
	if hb.Load1 != nil {
		t.Fatalf("load1 = %v from a stale sample, want unset", hb.GetLoad1())
	}
	// A sample whose load failed leaves load1 unset, not 0.
	rd := good()
	rd.Load = func(context.Context) (float64, float64, error) { return 0, 0, context.DeadlineExceeded }
	s2 := one(t, machine.SamplerOptions{StateDir: "/state", Readers: rd})
	hb = &agentv1.Heartbeat{}
	hooksFor(t, s2).Heartbeat(context.Background(), hb)
	if hb.Load1 != nil {
		t.Fatalf("load1 = %v after a failed load reading, want unset", hb.GetLoad1())
	}
}
