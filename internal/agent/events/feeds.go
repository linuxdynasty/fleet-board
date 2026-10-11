package events

import (
	"context"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/machine"
	"github.com/linuxdynasty/fleet-board/internal/agent/policy"
)

// PolicyChanges is policy.Options.OnChange: each hash change of
// policy.yaml becomes a policy_changed event (NEVER_DROP, audited; row 6
// ruling fork 1). It runs on the policy manager's report goroutine and waits
// only for the ring write, never for the stream. A failure is logged.
func PolicyChanges(e *Emitter) func(policy.Change) {
	return func(c policy.Change) {
		ev := &agentv1.Event{
			ObservedTime: timestamppb.New(c.Time),
			Body: &agentv1.Event_PolicyChanged{PolicyChanged: &agentv1.PolicyChanged{
				Policy: c.Info, PreviousHash: c.PreviousHash,
			}},
		}
		if _, err := e.Emit(context.Background(), ev); err != nil {
			e.log.Warn("ring.emit_failed", "kind", "policy_changed", "err_kind", "io")
		}
	}
}

// ResourceSamples emits a resource_sample event (SHED) for each sample the
// sampler publishes (machine.Sampler.Subscribe, every 30 s) until ctx ends.
// The event carries a copy: the published sample is shared.
func ResourceSamples(ctx context.Context, e *Emitter, samples <-chan *machine.Sample) {
	for {
		select {
		case <-ctx.Done():
			return
		case smp := <-samples:
			rs, ok := proto.Clone(smp.Resources).(*agentv1.ResourceSample)
			if !ok {
				continue
			}
			ev := &agentv1.Event{
				ObservedTime: timestamppb.New(smp.Taken),
				Body:         &agentv1.Event_ResourceSample{ResourceSample: rs},
			}
			if _, err := e.Emit(ctx, ev); err != nil && ctx.Err() == nil {
				e.log.WarnContext(ctx, "ring.emit_failed", "kind", "resource_sample", "err_kind", "io")
			}
		}
	}
}
