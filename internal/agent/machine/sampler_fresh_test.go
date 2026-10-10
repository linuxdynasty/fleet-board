package machine_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/machine"
)

// bootClock is a hand-set clock that keeps counting while the machine sleeps
// (CLOCK_BOOTTIME on Linux). The test clock for wall time has no monotonic
// reading, so the two can disagree, as they do after a sleep.
type bootClock struct {
	mu  sync.Mutex
	d   time.Duration
	err error
}

func (b *bootClock) read() (time.Duration, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.d, b.err
}

func (b *bootClock) add(d time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.d += d
}

func (b *bootClock) fail(err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.err = err
}

// The age of a sample is the larger of the wall (monotonic) age and the age on
// the clock that counts sleep (02i "Clocks"). A machine that slept for hours
// has a monotonic clock that did not move, so a sample from before the sleep
// must not look new on wake.
func TestFreshCountsTheTimeTheMachineWasAsleep(t *testing.T) {
	t.Parallel()
	c := newClock()
	b := &bootClock{d: 100 * time.Hour}
	s := one(t, machine.SamplerOptions{StateDir: "/state", Readers: good(), Now: c.now, Boot: b.read})
	if s.Fresh() == nil {
		t.Fatal("a sample taken now is not fresh")
	}
	c.add(60 * time.Second)
	b.add(60 * time.Second)
	if s.Fresh() == nil {
		t.Fatal("60 s on both clocks is still fresh")
	}
	b.add(8 * time.Hour) // asleep: only the boot clock moved
	if s.Fresh() != nil {
		t.Fatal("a sample from before 8 h of sleep was reported as current")
	}
}

func TestFreshIsUnknownWhenTheSleepClockCannotBeRead(t *testing.T) {
	t.Parallel()
	c := newClock()
	// Unreadable when the sample was taken.
	b := &bootClock{err: errors.New("no boot clock")}
	s := one(t, machine.SamplerOptions{StateDir: "/state", Readers: good(), Now: c.now, Boot: b.read})
	b.fail(nil) // it reads 0 now: the same as the 0 a sample without a reading holds
	if s.Fresh() != nil {
		t.Fatal("a sample taken without a sleep-clock reading cannot be vouched for")
	}
	// Unreadable only now.
	b2 := &bootClock{d: time.Hour}
	s2 := one(t, machine.SamplerOptions{StateDir: "/state", Readers: good(), Now: c.now, Boot: b2.read})
	if s2.Fresh() == nil {
		t.Fatal("not fresh with a working clock")
	}
	b2.fail(errors.New("no boot clock"))
	if s2.Fresh() != nil {
		t.Fatal("with the sleep clock unreadable the sample is unknown (02i)")
	}
	// A boot clock that went backwards cannot be trusted either.
	b3 := &bootClock{d: time.Hour}
	s3 := one(t, machine.SamplerOptions{StateDir: "/state", Readers: good(), Now: c.now, Boot: b3.read})
	b3.add(-time.Minute)
	if s3.Fresh() != nil {
		t.Fatal("a sleep clock that went backwards must make the sample unknown")
	}
}

// A sampler that stops producing leaves a stale sample, and nothing else
// says so: the Snapshot just carries no resources. So the withholding is
// counted every time and logged once per run of staleness.
func TestWithholdingAStaleSampleIsSignalled(t *testing.T) {
	t.Parallel()
	tel, m, logs := newTel(t)
	c := newClock()
	b := &bootClock{d: time.Hour}
	s := one(t, machine.SamplerOptions{StateDir: "/state", Readers: good(), Now: c.now, Boot: b.read, Metrics: m})
	if s.Fresh() == nil || len(logs.events("sample.stale")) != 0 {
		t.Fatal("a fresh sample must not be signalled")
	}
	c.add(time.Hour)
	for range 3 {
		if s.Fresh() != nil {
			t.Fatal("an hour-old sample is fresh")
		}
	}
	ev := logs.events("sample.stale")
	if len(ev) != 1 || ev[0]["reason"] != "age" || ev[0]["level"] != "warn" {
		t.Fatalf("want one warn sample.stale with reason age, got %v", ev)
	}
	if v := seriesValue(scrape(t, tel), "fleet_agent_sample_stale_total{", `reason="age"`); v != "3" {
		t.Fatalf("stale counter = %q, want 3", v)
	}
	// The sleep clock failing is its own reason.
	b.fail(errors.New("no boot clock"))
	if s.Fresh() != nil {
		t.Fatal("fresh with no sleep clock")
	}
	if v := seriesValue(scrape(t, tel), "fleet_agent_sample_stale_total{", `reason="boot_clock"`); v != "1" {
		t.Fatalf("boot_clock stale counter = %q, want 1", v)
	}
}

func TestAFreshSampleAfterStalenessIsLoggedOnce(t *testing.T) {
	t.Parallel()
	_, m, logs := newTel(t)
	c := newClock()
	b := &bootClock{d: time.Hour}
	k := newTicker()
	s, err := machine.NewSampler(machine.SamplerOptions{StateDir: "/state", Readers: good(), Now: c.now, Boot: b.read, Metrics: m, Ticks: k.fn})
	if err != nil {
		t.Fatal(err)
	}
	sub, unsub := s.Subscribe()
	defer unsub()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	<-sub
	c.add(time.Hour)
	_ = s.Fresh() // stale
	k.ch <- time.Now()
	<-sub
	for range 2 {
		if s.Fresh() == nil {
			t.Fatal("a new sample is fresh")
		}
	}
	cancel()
	<-done
	if n := len(logs.events("sample.fresh")); n != 1 {
		t.Fatalf("sample.fresh logged %d times, want once", n)
	}
}

// A new subscriber is not handed a sample that Fresh would withhold.
func TestSubscribeDoesNotHandOutAStaleSample(t *testing.T) {
	t.Parallel()
	c := newClock()
	b := &bootClock{d: time.Hour}
	s := one(t, machine.SamplerOptions{StateDir: "/state", Readers: good(), Now: c.now, Boot: b.read})
	b.add(time.Hour)
	sub, unsub := s.Subscribe()
	defer unsub()
	select {
	case smp := <-sub:
		t.Fatalf("a stale sample was handed out: %v", smp.Taken)
	default:
	}
}
