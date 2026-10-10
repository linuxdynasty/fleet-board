package machine

import "context"

// SampleOnceForBench takes one sample without the loop, for the benchmarks.
func (s *Sampler) SampleOnceForBench(ctx context.Context) { s.sample(ctx) }

// ReadsInFlight is the number of readings started and not yet returned.
func (s *Sampler) ReadsInFlight() int {
	s.busyMu.Lock()
	defer s.busyMu.Unlock()
	return len(s.busy)
}
