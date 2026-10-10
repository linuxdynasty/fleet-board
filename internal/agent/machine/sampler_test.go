package machine_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/machine"
)

// good is a set of readers that all work.
func good() machine.Readers {
	return machine.Readers{
		Load: func(context.Context) (float64, float64, error) { return 0.25, 0.5, nil },
		Mem:  func(context.Context) (uint64, uint64, error) { return 16 << 30, 6 << 30, nil },
		Pressure: func(_ context.Context, total, avail uint64, ok bool) (uint32, error) {
			if !ok || total != 16<<30 || avail != 6<<30 {
				return 0, errors.New("pressure got the wrong memory reading")
			}
			return 2, nil
		},
		Usage: func(_ context.Context, p string) (uint64, uint64, error) { return uint64(len(p)) * 1000, 1 << 40, nil },
	}
}

// clock is a hand-set clock.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func newClock() *clock { return &clock{t: time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)} }

// ticker is a hand-fired pace for Run.
type ticker struct{ ch chan time.Time }

func newTicker() *ticker { return &ticker{ch: make(chan time.Time)} }

func (k *ticker) fn(time.Duration) (<-chan time.Time, func()) { return k.ch, func() {} }

// one runs a single sample: Run with a context cancelled once the first
// sample is out.
func one(t *testing.T, o machine.SamplerOptions) *machine.Sampler {
	t.Helper()
	k := newTicker()
	o.Ticks = k.fn
	s, err := machine.NewSampler(o)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	sub, unsub := s.Subscribe()
	defer unsub()
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	select {
	case <-sub:
	case <-time.After(10 * time.Second):
		t.Fatal("no sample")
	}
	cancel()
	<-done
	return s
}

func TestSampleHasEveryReading(t *testing.T) {
	t.Parallel()
	s := one(t, machine.SamplerOptions{StateDir: "/state", Roots: func() []string { return []string{"/repos/a", "/repos/b"} }, Readers: good()})
	r := s.Latest().Resources
	if r.GetLoad1() != 0.25 || r.GetLoad5() != 0.5 || r.GetMemTotalBytes() != 16<<30 || r.GetMemAvailableBytes() != 6<<30 || r.GetMemPressureLevel() != 2 {
		t.Fatalf("resources = %v", r)
	}
	var got []string
	for _, d := range r.GetDisk() {
		got = append(got, fmt.Sprintf("%s:%d:%d", d.GetPath(), d.GetFreeBytes(), d.GetTotalBytes()))
	}
	want := []string{"/state:6000:1099511627776", "/repos/a:8000:1099511627776", "/repos/b:8000:1099511627776"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("disk = %v, want %v (state folder first, then the roots in order)", got, want)
	}
	if s.Latest() == nil {
		t.Fatal("no sample")
	}
}

// A reading that fails is unset in the sample, not 0 (principle 3), and the
// other readings are still there.
func TestAFailedReadingIsUnsetNotZero(t *testing.T) {
	t.Parallel()
	fail := errors.New("boom")
	tests := []struct {
		name   string
		mutate func(*machine.Readers)
		check  func(*agentv1.ResourceSample) string
	}{
		{"load", func(r *machine.Readers) {
			r.Load = func(context.Context) (float64, float64, error) { return 0, 0, fail }
		}, func(r *agentv1.ResourceSample) string {
			if r.Load1 != nil || r.Load5 != nil || r.MemTotalBytes == nil || r.MemPressureLevel == nil || len(r.GetDisk()) == 0 {
				return "load must be unset and the rest set"
			}
			return ""
		}},
		{"mem", func(r *machine.Readers) {
			r.Mem = func(context.Context) (uint64, uint64, error) { return 0, 0, fail }
			r.Pressure = func(_ context.Context, _, _ uint64, ok bool) (uint32, error) {
				if ok {
					return 0, errors.New("pressure was told the memory reading worked")
				}
				return 1, nil
			}
		}, func(r *agentv1.ResourceSample) string {
			if r.MemTotalBytes != nil || r.MemAvailableBytes != nil || r.Load1 == nil || r.GetMemPressureLevel() != 1 {
				return "memory must be unset and the rest set"
			}
			return ""
		}},
		{"pressure", func(r *machine.Readers) {
			r.Pressure = func(context.Context, uint64, uint64, bool) (uint32, error) { return 0, fail }
		}, func(r *agentv1.ResourceSample) string {
			if r.MemPressureLevel != nil || r.MemTotalBytes == nil || r.Load1 == nil {
				return "the level must be unset and the rest set"
			}
			return ""
		}},
		{"one disk path", func(r *machine.Readers) {
			r.Usage = func(_ context.Context, p string) (uint64, uint64, error) {
				if p == "/repos/a" {
					return 0, 0, fail
				}
				return 1, 2, nil
			}
		}, func(r *agentv1.ResourceSample) string {
			if len(r.GetDisk()) != 2 || r.GetDisk()[0].GetPath() != "/state" || r.GetDisk()[1].GetPath() != "/repos/b" {
				return fmt.Sprintf("disk must list only the paths that were read, got %v", r.GetDisk())
			}
			return ""
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rd := good()
			tc.mutate(&rd)
			s := one(t, machine.SamplerOptions{StateDir: "/state", Roots: func() []string { return []string{"/repos/a", "/repos/b"} }, Readers: rd})
			if msg := tc.check(s.Latest().Resources); msg != "" {
				t.Fatal(msg)
			}
		})
	}
}

// A sample in which nothing could be read is not published: there is nothing
// to say, and the old one ages out.
func TestASampleWithNoReadingIsNotPublished(t *testing.T) {
	t.Parallel()
	fail := errors.New("boom")
	rd := machine.Readers{
		Load:     func(context.Context) (float64, float64, error) { return 0, 0, fail },
		Mem:      func(context.Context) (uint64, uint64, error) { return 0, 0, fail },
		Pressure: func(context.Context, uint64, uint64, bool) (uint32, error) { return 0, fail },
		Usage:    func(context.Context, string) (uint64, uint64, error) { return 0, 0, fail },
	}
	k := newTicker()
	s, err := machine.NewSampler(machine.SamplerOptions{StateDir: "/state", Readers: rd, Ticks: k.fn})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	k.ch <- time.Now() // the second sample can only start after the first ended
	cancel()
	<-done
	if s.Latest() != nil || s.Fresh() != nil {
		t.Fatalf("Latest = %v, want nil", s.Latest())
	}
}

func TestFreshIsNilOnceTheSampleIsOlderThanThreeIntervals(t *testing.T) {
	t.Parallel()
	c := newClock()
	s := one(t, machine.SamplerOptions{StateDir: "/state", Readers: good(), Now: c.now, Interval: 30 * time.Second})
	if s.Fresh() == nil {
		t.Fatal("a sample taken now is not fresh")
	}
	c.add(90 * time.Second)
	if s.Fresh() == nil {
		t.Fatal("a sample exactly three intervals old is still fresh")
	}
	c.add(time.Second)
	if s.Fresh() != nil {
		t.Fatal("a sample over three intervals old must not be reported as the current one")
	}
	if s.Latest() == nil {
		t.Fatal("Latest keeps the last sample")
	}
}

func TestRunSamplesAtStartThenOnEveryTick(t *testing.T) {
	t.Parallel()
	var calls atomic.Int64
	rd := good()
	rd.Load = func(context.Context) (float64, float64, error) { return float64(calls.Add(1)), 0, nil }
	k := newTicker()
	s, err := machine.NewSampler(machine.SamplerOptions{StateDir: "/state", Readers: rd, Ticks: k.fn})
	if err != nil {
		t.Fatal(err)
	}
	sub, unsub := s.Subscribe()
	defer unsub()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	for want := 1.0; want <= 3; want++ {
		select {
		case smp := <-sub:
			if smp.Resources.GetLoad1() != want {
				t.Fatalf("sample %v has load1 %v", want, smp.Resources.GetLoad1())
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("no sample %v", want)
		}
		if want < 3 {
			k.ch <- time.Now()
		}
	}
	cancel()
	<-done
	if calls.Load() != 3 {
		t.Fatalf("readings = %d, want 3 (one at start, one per tick)", calls.Load())
	}
}

// A subscriber that does not read keeps only the newest sample, and the
// sampler never waits for it (resource_sample is SHED).
func TestASlowSubscriberKeepsOnlyTheNewestSample(t *testing.T) {
	t.Parallel()
	var n atomic.Int64
	rd := good()
	rd.Load = func(context.Context) (float64, float64, error) { return float64(n.Add(1)), 0, nil }
	k := newTicker()
	s, err := machine.NewSampler(machine.SamplerOptions{StateDir: "/state", Readers: rd, Ticks: k.fn})
	if err != nil {
		t.Fatal(err)
	}
	sub, unsub := s.Subscribe()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	fast, unfast := s.Subscribe() // one that reads, to know when the last sample is out
	defer unfast()
	for range 4 { // the start sample plus 4 ticks, unread by sub
		k.ch <- time.Now()
	}
	for (<-fast).Resources.GetLoad1() != 5 {
	}
	cancel()
	<-done
	if got := (<-sub).Resources.GetLoad1(); got != 5 {
		t.Fatalf("the subscriber saw sample %v, want the newest (5)", got)
	}
	select {
	case extra := <-sub:
		t.Fatalf("a second sample was queued: %v", extra.Resources)
	default:
	}
	unsub()
	// A subscriber that left gets nothing and is not waited on.
	k2 := newTicker()
	s2, _ := machine.NewSampler(machine.SamplerOptions{StateDir: "/state", Readers: good(), Ticks: k2.fn})
	sub2, unsub2 := s2.Subscribe()
	unsub2()
	ctx2, cancel2 := context.WithCancel(t.Context())
	d2 := make(chan struct{})
	go func() { s2.Run(ctx2); close(d2) }()
	k2.ch <- time.Now()
	cancel2()
	<-d2
	select {
	case <-sub2:
		t.Fatal("an unsubscribed channel received a sample")
	default:
	}
}

func TestASubscriberGetsTheCurrentSampleAtOnce(t *testing.T) {
	t.Parallel()
	s := one(t, machine.SamplerOptions{StateDir: "/state", Readers: good()})
	sub, unsub := s.Subscribe()
	defer unsub()
	select {
	case smp := <-sub:
		if smp != s.Latest() {
			t.Fatal("a new subscriber must get the latest sample")
		}
	default:
		t.Fatal("no current sample delivered")
	}
}

func TestDiskPathsAreDedupedAndCapped(t *testing.T) {
	t.Parallel()
	// Duplicates and an empty root come first, so a missing dedupe would show
	// as the same path twice among the first entries.
	roots := []string{"/state/", "/repos/r00/", "", "/repos/r00"}
	for i := range machine.MaxDiskPaths + 5 {
		roots = append(roots, fmt.Sprintf("/repos/r%02d", i))
	}
	tel, m, logs := newTel(t)
	s := one(t, machine.SamplerOptions{StateDir: "/state", Roots: func() []string { return roots }, Readers: good(), Metrics: m})
	disk := s.Latest().Resources.GetDisk()
	if len(disk) != machine.MaxDiskPaths {
		t.Fatalf("disk entries = %d, want the cap %d", len(disk), machine.MaxDiskPaths)
	}
	if disk[0].GetPath() != "/state" || disk[1].GetPath() != "/repos/r00" || disk[2].GetPath() != "/repos/r01" {
		t.Fatalf("order/dedupe wrong: %v %v %v", disk[0].GetPath(), disk[1].GetPath(), disk[2].GetPath())
	}
	if ev := logs.events("sample.failed"); len(ev) != 1 || ev[0]["source"] != "disk" || ev[0]["err_kind"] != "constraint" {
		t.Fatalf("want one sample.failed disk/constraint, got %v", ev)
	}
	if v := seriesValue(scrape(t, tel), "fleet_agent_sample_failures_total{", `source="disk"`); v != "1" {
		t.Fatalf("failure counter = %q, want 1", v)
	}
}

// A statfs on a hung mount blocks in the kernel and ignores the context.
// The sample must still finish, keep the other paths, and must not start a
// second read of the stuck path on the next sample.
func TestAHungDiskPathDoesNotStopTheSample(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	var hungCalls atomic.Int64
	rd := good()
	rd.Usage = func(_ context.Context, p string) (uint64, uint64, error) {
		if p == "/mnt/hung" {
			hungCalls.Add(1)
			<-release
			return 1, 1, nil
		}
		return 7, 9, nil
	}
	t.Cleanup(func() { close(release) })
	_, m, logs := newTel(t)
	k := newTicker()
	s, err := machine.NewSampler(machine.SamplerOptions{
		StateDir: "/state", Roots: func() []string { return []string{"/mnt/hung", "/repos/a"} },
		Readers: rd, Metrics: m, Ticks: k.fn, ReadTimeout: 200 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	sub, unsub := s.Subscribe()
	defer unsub()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	first := <-sub
	k.ch <- time.Now()
	second := <-sub
	cancel()
	<-done
	for i, smp := range []*machine.Sample{first, second} {
		var paths []string
		for _, d := range smp.Resources.GetDisk() {
			paths = append(paths, d.GetPath())
		}
		if fmt.Sprint(paths) != "[/state /repos/a]" {
			t.Fatalf("sample %d disk = %v, want the paths that answered", i+1, paths)
		}
		if smp.Resources.Load1 == nil {
			t.Fatalf("sample %d lost its load reading", i+1)
		}
	}
	if hungCalls.Load() != 1 {
		t.Fatalf("the stuck path was read %d times, want 1 (no second read while the first has not returned)", hungCalls.Load())
	}
	if ev := logs.events("sample.failed"); len(ev) != 1 || ev[0]["err_kind"] != "timeout" {
		t.Fatalf("want one sample.failed with err_kind timeout, got %v", ev)
	}
}

func TestFailuresAreCountedEveryTimeLoggedOnceAndRecovery(t *testing.T) {
	t.Parallel()
	tel, m, logs := newTel(t)
	var failing atomic.Bool
	failing.Store(true)
	rd := good()
	rd.Load = func(context.Context) (float64, float64, error) {
		if failing.Load() {
			return 0, 0, fmt.Errorf("read /proc/loadavg of /home/secret-user: %w", errors.New("denied"))
		}
		return 1, 1, nil
	}
	k := newTicker()
	s, err := machine.NewSampler(machine.SamplerOptions{StateDir: "/state", Readers: rd, Metrics: m, Ticks: k.fn})
	if err != nil {
		t.Fatal(err)
	}
	sub, unsub := s.Subscribe()
	defer unsub()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	<-sub
	k.ch <- time.Now()
	<-sub
	k.ch <- time.Now()
	<-sub
	failing.Store(false)
	k.ch <- time.Now()
	<-sub
	cancel()
	<-done

	if v := seriesValue(scrape(t, tel), "fleet_agent_sample_failures_total{", `source="load"`); v != "3" {
		t.Fatalf("load failures counted = %q, want 3", v)
	}
	ev := logs.events("sample.failed")
	if len(ev) != 1 || ev[0]["source"] != "load" || ev[0]["err_kind"] != "io" || ev[0]["level"] != "warn" {
		t.Fatalf("want exactly one warn sample.failed for load/io, got %v", ev)
	}
	for _, l := range logs.lines() {
		for k, v := range l {
			if str, ok := v.(string); ok && (str == "denied" || strings.Contains(str, "secret-user")) {
				t.Fatalf("log field %s leaked error text: %q", k, str)
			}
		}
	}
	if rec := logs.events("sample.recovered"); len(rec) != 1 || rec[0]["source"] != "load" {
		t.Fatalf("want one sample.recovered for load, got %v", rec)
	}
	if v := seriesValue(scrape(t, tel), "fleet_agent_sample_seconds_count"); v != "4" {
		t.Fatalf("sample_seconds count = %q, want 4 samples", v)
	}
}

// The Heartbeat hook reads Latest on the sending goroutine while Run
// publishes: run under -race.
func TestLatestIsSafeToReadWhileSampling(t *testing.T) {
	t.Parallel()
	k := newTicker()
	s, err := machine.NewSampler(machine.SamplerOptions{StateDir: "/state", Readers: good(), Ticks: k.fn})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range 200 {
				if l := s.Fresh(); l != nil {
					_ = l.Resources.GetLoad1()
				}
				_ = s.Latest()
			}
		})
	}
	for range 20 {
		k.ch <- time.Now()
	}
	wg.Wait()
	cancel()
	<-done
}
