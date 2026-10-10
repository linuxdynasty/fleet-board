package machine

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/protobuf/proto"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
)

const (
	// DefaultInterval is the B2 sample interval (defaults.md "Resource
	// sample (B2)": 30 s).
	DefaultInterval = 30 * time.Second
	// DefaultDiskTimeout bounds one pass over the disk paths. A statfs on a
	// hung network mount cannot be cancelled, so the sampler stops waiting.
	DefaultDiskTimeout = 10 * time.Second
	// MaxDiskPaths is the most paths one sample reads: the state folder and
	// the allowed repo roots. More are not read and are reported once
	// (sample.failed, source disk, err_kind constraint).
	MaxDiskPaths = 32
	// staleAfterIntervals is how many intervals old a sample may be before
	// [Sampler.Fresh] stops vouching for it.
	staleAfterIntervals = 3
)

// Sample is one finished resource sample. It is immutable once published:
// readers share it, so a reader that needs to change it clones Resources.
type Sample struct {
	// Taken is when the sample finished, on the agent's clock.
	Taken time.Time
	// Resources has only the readings that worked; see ResourceSample.
	Resources *agentv1.ResourceSample
}

// SamplerOptions configure a [Sampler].
type SamplerOptions struct {
	// StateDir is the agent state folder, read for free space first.
	StateDir string
	// Roots returns the allowed repo roots now (policy, row 6b), read at each
	// sample. Nil means none.
	Roots func() []string
	// Interval defaults to [DefaultInterval]; DiskTimeout to
	// [DefaultDiskTimeout].
	Interval    time.Duration
	DiskTimeout time.Duration
	// Readers replaces individual readings; nil fields read the machine.
	Readers Readers
	// Metrics is where failures and cost are reported; nil reports nowhere.
	Metrics *Metrics
	// Now and Ticks are the clocks; nil is the real one. Ticks returns the
	// channel that paces the loop and a stop function.
	Now   func() time.Time
	Ticks func(d time.Duration) (<-chan time.Time, func())
}

// Sampler takes the B2 resource sample every interval and keeps the latest.
//
// Concurrency: Run is the only writer; it runs one goroutine and samples
// from it, plus at most one short-lived goroutine per disk path (see
// readDisk). The latest sample is an atomic pointer, so [Sampler.Latest] and
// [Sampler.Fresh] take no lock and are safe on the connection loop's sending
// goroutine (conn.LoopOptions.Heartbeat may only read what it holds). The
// subscriber set has one mutex, held only to copy or hand over a pointer.
type Sampler struct {
	o   SamplerOptions
	m   *Metrics
	now func() time.Time

	latest atomic.Pointer[Sample]

	subMu sync.Mutex
	subs  map[chan *Sample]struct{}

	diskMu   sync.Mutex
	diskBusy map[string]bool // path -> a read of it has not returned
}

// NewSampler returns a Sampler; call [Sampler.Run] to start it.
func NewSampler(o SamplerOptions) (*Sampler, error) {
	if o.Interval <= 0 {
		o.Interval = DefaultInterval
	}
	if o.DiskTimeout <= 0 {
		o.DiskTimeout = DefaultDiskTimeout
	}
	o.Readers = o.Readers.withDefaults()
	s := &Sampler{o: o, m: o.Metrics, now: o.Now, subs: map[chan *Sample]struct{}{}, diskBusy: map[string]bool{}}
	if s.now == nil {
		s.now = time.Now
	}
	if s.m == nil {
		m, err := NewMetrics(nil)
		if err != nil {
			return nil, err
		}
		s.m = m
	}
	if s.o.Ticks == nil {
		s.o.Ticks = func(d time.Duration) (<-chan time.Time, func()) {
			t := time.NewTicker(d)
			return t.C, t.Stop
		}
	}
	return s, nil
}

// Run takes a sample at once, so the first Snapshot after boot is not empty,
// then one every interval, until ctx ends. It is the Sampler's only loop.
func (s *Sampler) Run(ctx context.Context) {
	s.sample(ctx)
	ticks, stop := s.o.Ticks(s.o.Interval)
	defer stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			s.sample(ctx)
		}
	}
}

// Latest is the newest sample, or nil before the first one. It is shared and
// must not be changed.
func (s *Sampler) Latest() *Sample { return s.latest.Load() }

// Fresh is Latest when it is at most three intervals old, else nil. An old
// reading is no reading: a stuck sampler must not report last hour's load as
// now (principle 3).
func (s *Sampler) Fresh() *Sample {
	l := s.latest.Load()
	if l == nil || s.now().Sub(l.Taken) > staleAfterIntervals*s.o.Interval {
		return nil
	}
	return l
}

// Subscribe returns a channel that holds the newest sample, for row 7's
// resource_sample event (SHED: the next sample replaces an unread one), and a
// function that ends the subscription. The channel is never closed. The
// current sample, if any, is delivered at once.
func (s *Sampler) Subscribe() (<-chan *Sample, func()) {
	ch := make(chan *Sample, 1)
	s.subMu.Lock()
	s.subs[ch] = struct{}{}
	if l := s.latest.Load(); l != nil {
		ch <- l
	}
	s.subMu.Unlock()
	return ch, func() {
		s.subMu.Lock()
		delete(s.subs, ch)
		s.subMu.Unlock()
	}
}

// publish stores smp and hands it to every subscriber, replacing a sample
// nobody read yet. It never blocks.
func (s *Sampler) publish(smp *Sample) {
	s.latest.Store(smp)
	s.subMu.Lock()
	defer s.subMu.Unlock()
	for ch := range s.subs {
		select {
		case <-ch:
		default:
		}
		ch <- smp // the buffer is 1, the only sender holds subMu, and the slot was just emptied
	}
}

// sample takes one sample. A reading that fails stays unset in it, is counted,
// and is logged on its first failure; a sample with no reading at all is not
// published, so the previous one ages out through [Sampler.Fresh].
func (s *Sampler) sample(ctx context.Context) {
	start := s.now()
	t := newTally(s.m)
	r := &agentv1.ResourceSample{}
	rd := s.o.Readers
	got := false

	if l1, l5, err := rd.Load(ctx); err != nil {
		t.fail(ctx, sourceLoad, err)
	} else {
		r.Load1, r.Load5, got = proto.Float64(l1), proto.Float64(l5), true
		t.ok(sourceLoad)
	}
	total, avail, merr := rd.Mem(ctx)
	if merr != nil {
		t.fail(ctx, sourceMem, merr)
	} else {
		r.MemTotalBytes, r.MemAvailableBytes, got = proto.Uint64(total), proto.Uint64(avail), true
		t.ok(sourceMem)
	}
	if lvl, err := rd.Pressure(ctx, total, avail, merr == nil); err != nil {
		t.fail(ctx, sourceMemPress, err)
	} else {
		r.MemPressureLevel, got = proto.Uint32(lvl), true
		t.ok(sourceMemPress)
	}
	r.Disk = s.readDisk(ctx, t)
	if len(r.GetDisk()) > 0 {
		got = true
	}
	t.settle(ctx)

	end := s.now()
	s.m.took(ctx, end.Sub(start))
	if got {
		s.publish(&Sample{Taken: end, Resources: r})
	}
}

// diskPaths are the paths one sample reads: the state folder, then the repo
// roots, each once, at most [MaxDiskPaths].
func (s *Sampler) diskPaths() (paths []string, capped bool) {
	seen := map[string]bool{}
	add := func(p string) {
		if p == "" {
			return
		}
		p = filepath.Clean(p)
		if seen[p] {
			return
		}
		seen[p] = true
		if len(paths) == MaxDiskPaths {
			capped = true
			return
		}
		paths = append(paths, p)
	}
	add(s.o.StateDir)
	if s.o.Roots != nil {
		for _, p := range s.o.Roots() {
			add(p)
		}
	}
	return paths, capped
}

type diskResult struct {
	i           int
	path        string
	free, total uint64
	err         error
}

// readDisk reads free space under each path. Each read runs in a goroutine of
// its own because a statfs on a hung mount blocks in the kernel and ignores
// the context: the sample waits at most DiskTimeout, keeps what came back,
// and does not start another read of a path whose last read has not
// returned. So a hung mount costs at most one parked goroutine per path, never
// a pile, and never delays the load, memory or other paths' readings by more
// than the timeout.
func (s *Sampler) readDisk(ctx context.Context, t *tally) []*agentv1.DiskFree {
	paths, capped := s.diskPaths()
	if capped {
		t.fail(ctx, sourceDisk, errConstraint)
	}
	ch := make(chan diskResult, len(paths))
	started := 0
	for i, p := range paths {
		if !s.claimDisk(p) {
			t.fail(ctx, sourceDisk, errBusy)
			continue
		}
		started++
		go func() {
			defer s.releaseDisk(p)
			f, tot, err := s.o.Readers.Usage(ctx, p)
			ch <- diskResult{i: i, path: p, free: f, total: tot, err: err}
		}()
	}
	byIndex := make([]*agentv1.DiskFree, len(paths))
	timer := time.NewTimer(s.o.DiskTimeout)
	defer timer.Stop()
wait:
	for got := 0; got < started; got++ {
		select {
		case r := <-ch:
			if r.err != nil {
				t.fail(ctx, sourceDisk, r.err)
				continue
			}
			t.ok(sourceDisk)
			byIndex[r.i] = &agentv1.DiskFree{Path: r.path, FreeBytes: r.free, TotalBytes: r.total}
		case <-timer.C:
			t.fail(ctx, sourceDisk, context.DeadlineExceeded)
			break wait
		case <-ctx.Done():
			break wait
		}
	}
	out := make([]*agentv1.DiskFree, 0, len(paths))
	for _, d := range byIndex {
		if d != nil {
			out = append(out, d)
		}
	}
	return out
}

func (s *Sampler) claimDisk(path string) bool {
	s.diskMu.Lock()
	defer s.diskMu.Unlock()
	if s.diskBusy[path] {
		return false
	}
	s.diskBusy[path] = true
	return true
}

func (s *Sampler) releaseDisk(path string) {
	s.diskMu.Lock()
	delete(s.diskBusy, path)
	s.diskMu.Unlock()
}
