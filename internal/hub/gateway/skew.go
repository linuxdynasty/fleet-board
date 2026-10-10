package gateway

import (
	"context"
	"slices"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// skewSamples is how many samples the clock skew is the median of (02i
// "Clocks").
const skewSamples = 5

// skews keeps each machine's last [skewSamples] clock skew samples, in
// memory only: a restart starts again from the next Hello. The lock is
// held only for the slice work.
type skews struct {
	mu sync.Mutex
	by map[string][]time.Duration
}

func newSkews() *skews { return &skews{by: map[string][]time.Duration{}} }

// add records one sample for machineID and returns the median of its last
// [skewSamples].
func (k *skews) add(machineID string, d time.Duration) time.Duration {
	k.mu.Lock()
	defer k.mu.Unlock()
	s := append(k.by[machineID], d)
	if len(s) > skewSamples {
		s = s[len(s)-skewSamples:]
	}
	k.by[machineID] = s
	sorted := slices.Clone(s)
	slices.Sort(sorted)
	n := len(sorted)
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}

// skewSample takes one sample, agent time (sent) minus hub receive time
// (at), from a Hello or a Heartbeat, and sets
// fleet_hub_clock_skew_seconds{machine} to the median. A message without
// sent_time is no sample. The warning and clock_untrusted are plan row 5e's.
func (a *agentService) skewSample(ctx context.Context, machineID string, sent *timestamppb.Timestamp, at time.Time) {
	if sent == nil || !sent.IsValid() {
		return
	}
	median := a.skew.add(machineID, sent.AsTime().Sub(at))
	a.sm.skew.Record(ctx, median.Seconds(), metric.WithAttributes(attribute.String("machine", machineID)))
}
