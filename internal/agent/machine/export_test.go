package machine

import "context"

// SampleOnceForBench takes one sample without the loop, for the benchmarks.
func (s *Sampler) SampleOnceForBench(ctx context.Context) { s.sample(ctx) }
