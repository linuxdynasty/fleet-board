package machine_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/machine"
)

// run takes two samples (the start sample and one tick) and returns both.
func runTwo(t *testing.T, o machine.SamplerOptions) (first, second *machine.Sample) {
	t.Helper()
	k := newTicker()
	o.Ticks = k.fn
	s, err := machine.NewSampler(o)
	if err != nil {
		t.Fatal(err)
	}
	sub, unsub := s.Subscribe()
	defer unsub()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	first = <-sub
	k.ch <- time.Now()
	second = <-sub
	cancel()
	<-done
	return first, second
}

// A load or memory read that never returns (gopsutil takes no time bound) must
// not stall the sample or pile up goroutines: the sample waits ReadTimeout,
// keeps the other readings, and does not start a second read of a source whose
// last read is still out.
func TestAHungLoadOrMemoryReadDoesNotStopTheSample(t *testing.T) {
	t.Parallel()
	for _, source := range []string{"load", "mem"} {
		t.Run(source, func(t *testing.T) {
			t.Parallel()
			release := make(chan struct{})
			t.Cleanup(func() { close(release) })
			var calls atomic.Int64
			rd := good()
			switch source {
			case "load":
				rd.Load = func(context.Context) (float64, float64, error) { calls.Add(1); <-release; return 1, 1, nil }
			case "mem":
				rd.Mem = func(context.Context) (uint64, uint64, error) { calls.Add(1); <-release; return 1, 1, nil }
				rd.Pressure = func(_ context.Context, _, _ uint64, ok bool) (uint32, error) {
					if ok {
						return 0, errors.New("told a hung memory read worked")
					}
					return 1, nil
				}
			}
			_, m, logs := newTel(t)
			first, second := runTwo(t, machine.SamplerOptions{StateDir: "/state", Readers: rd, Metrics: m, ReadTimeout: 200 * time.Millisecond})
			for i, smp := range []*machine.Sample{first, second} {
				r := smp.Resources
				if source == "load" && (r.Load1 != nil || r.MemTotalBytes == nil) {
					t.Fatalf("sample %d: load must be unset and memory set: %v", i+1, r)
				}
				if source == "mem" && (r.MemTotalBytes != nil || r.Load1 == nil) {
					t.Fatalf("sample %d: memory must be unset and load set: %v", i+1, r)
				}
				if len(r.GetDisk()) != 1 {
					t.Fatalf("sample %d lost its disk reading", i+1)
				}
			}
			if calls.Load() != 1 {
				t.Fatalf("the stuck %s was read %d times, want 1", source, calls.Load())
			}
			ev := logs.events("sample.failed")
			if len(ev) != 1 || ev[0]["source"] != source || ev[0]["err_kind"] != "timeout" {
				t.Fatalf("want one sample.failed %s/timeout, got %v", source, ev)
			}
		})
	}
}

// Two stuck disk paths in one sample: the wait ends at the timeout (not once
// per path), both are reported, the paths that answered are kept.
func TestTwoHungDiskPathsInOneSample(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	rd := good()
	rd.Usage = func(_ context.Context, p string) (uint64, uint64, error) {
		if p == "/mnt/a" || p == "/mnt/b" {
			<-release
		}
		return 7, 9, nil
	}
	tel, m, _ := newTel(t)
	start := time.Now()
	k := newTicker()
	s, err := machine.NewSampler(machine.SamplerOptions{
		StateDir: "/state", Roots: func() []string { return []string{"/mnt/a", "/repos/x", "/mnt/b", "/repos/y"} },
		Readers: rd, Metrics: m, Ticks: k.fn, ReadTimeout: 300 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	sub, unsub := s.Subscribe()
	defer unsub()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	smp := <-sub
	cancel()
	<-done
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("the sample took %v with two stuck paths", took)
	}
	var paths []string
	for _, d := range smp.Resources.GetDisk() {
		paths = append(paths, d.GetPath())
	}
	if fmt.Sprint(paths) != "[/state /repos/x /repos/y]" {
		t.Fatalf("disk = %v, want the three paths that answered, in order", paths)
	}
	if v := seriesValue(scrape(t, tel), "fleet_agent_sample_failures_total{", `source="disk"`); v != "2" {
		t.Fatalf("disk failures counted = %q, want 2 (one per stuck path)", v)
	}
}

// A reading that cannot be true is not a reading.
func TestImpossibleReadingsAreUnknown(t *testing.T) {
	t.Parallel()
	rd := good()
	rd.Usage = func(_ context.Context, p string) (uint64, uint64, error) {
		switch p {
		case "/zero":
			return 0, 0, nil // a filesystem that reports no size
		case "/over":
			return 10, 5, nil // free above total
		}
		return 1, 2, nil
	}
	rd.Mem = func(context.Context) (uint64, uint64, error) { return 100, 200, nil } // available above total
	rd.Pressure = func(context.Context, uint64, uint64, bool) (uint32, error) { return 1, nil }
	s := one(t, machine.SamplerOptions{StateDir: "/state", Roots: func() []string { return []string{"/zero", "/over"} }, Readers: rd})
	r := s.Latest().Resources
	if len(r.GetDisk()) != 1 || r.GetDisk()[0].GetPath() != "/state" {
		t.Fatalf("disk = %v, want only /state", r.GetDisk())
	}
	if r.MemTotalBytes != nil || r.MemAvailableBytes != nil {
		t.Fatalf("memory with available above total was reported: %v", r)
	}
}

// When the agent stops, readings cut short by the cancelled context are not
// failures and make no sample: nothing is counted, logged or published at
// shutdown. Whether the loop sees the cancel or a finished reading first is a
// race, so the sample is taken many times; each outcome must be quiet.
func TestShutdownIsQuiet(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"readings fail", "readings succeed"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			tel, m, logs := newTel(t)
			var mu sync.Mutex
			var cur context.CancelFunc
			stop := func() { mu.Lock(); defer mu.Unlock(); cur() }
			fail := name == "readings fail"
			rd := good()
			rd.Load = func(context.Context) (float64, float64, error) {
				stop() // the agent is told to stop while this reading is out
				if fail {
					return 0, 0, context.Canceled
				}
				return 1, 1, nil
			}
			rd.Mem = func(ctx context.Context) (uint64, uint64, error) {
				<-ctx.Done()
				if fail {
					return 0, 0, context.Canceled
				}
				return 2, 1, nil
			}
			rd.Usage = func(ctx context.Context, _ string) (uint64, uint64, error) {
				<-ctx.Done()
				if fail {
					return 0, 0, context.Canceled
				}
				return 1, 2, nil
			}
			s, err := machine.NewSampler(machine.SamplerOptions{StateDir: "/state", Readers: rd, Metrics: m, ReadTimeout: time.Minute})
			if err != nil {
				t.Fatal(err)
			}
			for range 200 {
				ctx, cancel := context.WithCancel(t.Context())
				mu.Lock()
				cur = cancel
				mu.Unlock()
				s.SampleOnceForBench(ctx)
				cancel()
				for s.ReadsInFlight() > 0 { // let this round's readers end before the next starts
					time.Sleep(time.Millisecond)
				}
			}
			if ev := logs.events("sample.failed"); len(ev) != 0 {
				t.Fatalf("shutdown logged failures: %v", ev)
			}
			if v := seriesValue(scrape(t, tel), "fleet_agent_sample_failures_total{"); v != "" {
				t.Fatalf("shutdown counted a failure: %q", v)
			}
			if s.Latest() != nil {
				t.Fatal("a sample cut short by shutdown was published")
			}
		})
	}
}
