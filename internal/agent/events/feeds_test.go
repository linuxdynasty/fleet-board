package events_test

import (
	"context"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/events"
	"github.com/linuxdynasty/fleet-board/internal/agent/machine"
	"github.com/linuxdynasty/fleet-board/internal/agent/policy"
)

// Row 6 ruling fork 1: policy_changed comes from the policy loader's
// Changed seam, NEVER_DROP, so it is in the ring when the callback returns.
func TestAPolicyChangeIsInTheRingWhenTheCallbackReturns(t *testing.T) {
	t.Parallel()
	r := newRig(t, stateDir(t), events.Options{})
	at := time.Date(2026, 10, 2, 3, 4, 5, 0, time.UTC)
	events.PolicyChanges(r.e)(policy.Change{
		PreviousHash: "old", Hash: "new", Time: at, Info: &agentv1.PolicyInfo{Hash: "new", ChangedSinceLoad: true},
	})
	c, err := r.e.Attach(t.Context(), 0, 1<<40)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Detach()
	got := drain(t, c)
	if len(got) != 1 {
		t.Fatalf("ring holds %d events", len(got))
	}
	pc := got[0].GetPolicyChanged()
	if pc.GetPreviousHash() != "old" || pc.GetPolicy().GetHash() != "new" || !pc.GetPolicy().GetChangedSinceLoad() ||
		!got[0].GetObservedTime().AsTime().Equal(at) || got[0].GetDelivery() != agentv1.DeliveryClass_DELIVERY_CLASS_NEVER_DROP {
		t.Errorf("event %v", got[0])
	}
}

// resource_sample every sample (30 s), SHED: sent while a stream is
// attached, a copy of the sample (the published one is shared).
func TestEachSampleIsSentAsAShedResourceSample(t *testing.T) {
	t.Parallel()
	r := newRig(t, stateDir(t), events.Options{})
	c, err := r.e.Attach(t.Context(), 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Detach()
	ch := make(chan *machine.Sample, 1)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { events.ResourceSamples(ctx, r.e, ch); close(done) }()
	taken := time.Date(2026, 10, 2, 0, 0, 30, 0, time.UTC)
	smp := &machine.Sample{Taken: taken, Resources: &agentv1.ResourceSample{Load1: proto.Float64(1.5)}}
	ch <- smp
	var got []*agentv1.Event
	for len(got) == 0 {
		<-r.e.Ready()
		evs, _, err := c.Next(t.Context(), 8)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, evs...)
	}
	cancel()
	<-done
	rs := got[0].GetResourceSample()
	if rs.GetLoad1() != 1.5 || got[0].GetDelivery() != agentv1.DeliveryClass_DELIVERY_CLASS_SHED || !got[0].GetObservedTime().AsTime().Equal(taken) {
		t.Errorf("event %v", got[0])
	}
	if rs == smp.Resources {
		t.Error("the event shares the published sample")
	}
	if s, _ := r.db.EventsSize(t.Context()); s.Events != 0 {
		t.Errorf("a SHED sample reached the ring (%d entries)", s.Events)
	}
}
