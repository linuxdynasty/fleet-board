package machine_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/machine"
)

// A reading that has returned is never "still out": a tick that comes right
// after a sample must read load again. Run back to back with trivial readers,
// every sample that reached the pressure step started its load reading, so
// the load readings are never fewer than the samples. (If the sampler kept a
// key busy a moment after it handed its result over, the next sample would
// skip load, count a failure, and publish with no load1.)
func TestAFinishedReadingIsNotBusyForTheNextSample(t *testing.T) {
	t.Parallel()
	const ticks = 20000
	var loads, samples atomic.Int64
	rd := good()
	rd.Load = func(context.Context) (float64, float64, error) { loads.Add(1); return 1, 1, nil }
	press := rd.Pressure
	rd.Pressure = func(ctx context.Context, total, avail uint64, ok bool) (uint32, error) {
		samples.Add(1)
		return press(ctx, total, avail, ok)
	}
	k := newTicker()
	s, err := machine.NewSampler(machine.SamplerOptions{StateDir: "/state", Readers: rd, Ticks: k.fn, ReadTimeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	for range ticks {
		k.ch <- time.Now()
	}
	cancel()
	<-done
	if l, smp := loads.Load(), samples.Load(); l < smp {
		t.Fatalf("%d samples finished but only %d load readings started: a finished reading was still busy", smp, l)
	}
}
