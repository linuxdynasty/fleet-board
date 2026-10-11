package machine

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/protobuf/proto"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/wakenet"
)

const (
	// DefaultInterval is the B2 sample interval (defaults.md "Resource
	// sample (B2)": 30 s).
	DefaultInterval = 30 * time.Second
	// DefaultReadTimeout bounds one sample's wait for its readings. A statfs
	// on a hung network mount, or a stuck /proc read, cannot be cancelled, so
	// the sampler stops waiting.
	DefaultReadTimeout = 10 * time.Second
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
	// Taken is when the sample began (its readings were taken after it), on
	// the agent's clock.
	Taken time.Time
	// Resources has only the readings that worked; see ResourceSample.
	Resources *agentv1.ResourceSample

	// boot is the clock that keeps counting in sleep at Taken; bootOK is
	// false when it could not be read, which makes the sample unknowable.
	boot   time.Duration
	bootOK bool
}

// SamplerOptions configure a [Sampler].
type SamplerOptions struct {
	// StateDir is the agent state folder, read for free space first.
	StateDir string
	// Roots returns the allowed repo roots now (policy, row 6b), read at each
	// sample. Nil means none.
	Roots func() []string
	// Interval defaults to [DefaultInterval]; ReadTimeout to
	// [DefaultReadTimeout].
	Interval    time.Duration
	ReadTimeout time.Duration
	// Readers replaces individual readings; nil fields read the machine.
	Readers Readers
	// Metrics is where failures and cost are reported; nil reports nowhere.
	Metrics *Metrics
	// Now, Boot and Ticks are the clocks; nil is the real one. Boot is the
	// clock that keeps counting while the machine sleeps (wakenet.SinceBoot);
	// a sample's age is the larger of its age on Now and on Boot, and a
	// sample whose Boot could not be read is unknown (02i "Clocks"). Ticks
	// returns the channel that paces the loop and a stop function.
	Now   func() time.Time
	Boot  func() (time.Duration, error)
	Ticks func(d time.Duration) (<-chan time.Time, func())
}

// Sampler takes the B2 resource sample every interval and keeps the latest.
//
// Concurrency: Run is the only writer; it runs one goroutine and samples
// from it, plus one goroutine per reading in flight (see launch). The latest sample is an atomic pointer, so [Sampler.Latest] and
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

	busyMu sync.Mutex
	busy   map[string]bool // reading key -> a read of it has not returned
}

// NewSampler returns a Sampler; call [Sampler.Run] to start it.
func NewSampler(o SamplerOptions) (*Sampler, error) {
	if o.Interval <= 0 {
		o.Interval = DefaultInterval
	}
	if o.ReadTimeout <= 0 {
		o.ReadTimeout = DefaultReadTimeout
	}
	if o.Boot == nil {
		o.Boot = wakenet.SinceBoot
	}
	o.Readers = o.Readers.withDefaults()
	s := &Sampler{o: o, m: o.Metrics, now: o.Now, subs: map[chan *Sample]struct{}{}, busy: map[string]bool{}}
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

// Run takes a sample at once and then one every interval, until ctx ends. It
// is the Sampler's only loop. The first Snapshot of a run can still go out
// before the first sample is done (a connect can beat a slow reading), and
// then carries no resources; row 7's resource_sample event is what fills that
// gap (README).
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
// reading is no reading: a stuck sampler, or a machine that slept for hours,
// must not report old load as now (principle 3). Age is the larger of the age
// on the agent's clock (monotonic, which stands still in sleep) and on the
// boot clock (which does not); a sample or a moment with no boot-clock
// reading is unknown (02i "Clocks"). Withholding a sample is counted and
// logged once per run of staleness (sample.stale).
func (s *Sampler) Fresh() *Sample {
	l, reason := s.fresh()
	switch {
	case l != nil:
		s.m.freshAgain(context.Background())
	case reason != "":
		s.m.stale(context.Background(), reason)
	}
	return l
}

// fresh is Fresh without the signal. The reason is "" for a fresh sample and
// when there is no sample yet, else "age" or "boot_clock".
func (s *Sampler) fresh() (smp *Sample, reason string) {
	l := s.latest.Load()
	if l == nil {
		return nil, ""
	}
	age := s.now().Sub(l.Taken)
	if !l.bootOK {
		return nil, "boot_clock"
	}
	boot, err := s.o.Boot()
	if err != nil || boot < l.boot {
		return nil, "boot_clock"
	}
	age = max(age, boot-l.boot)
	if age > staleAfterIntervals*s.o.Interval {
		return nil, "age"
	}
	return l, ""
}

// Subscribe returns a channel that holds the newest sample, for row 7's
// resource_sample event (SHED: the next sample replaces an unread one), and a
// function that ends the subscription. The channel is never closed. The
// current sample is delivered at once if it is fresh.
func (s *Sampler) Subscribe() (<-chan *Sample, func()) {
	ch := make(chan *Sample, 1)
	s.subMu.Lock()
	s.subs[ch] = struct{}{}
	if l, _ := s.fresh(); l != nil {
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

// reading is what one reader goroutine hands back.
type reading struct {
	key, source string
	val         any
	err         error
}

type (
	loadVal struct{ l1, l5 float64 }
	memVal  struct{ total, avail uint64 }
	diskVal struct {
		i           int
		path        string
		free, total uint64
	}
)

// launch starts one reading in a goroutine of its own, unless the last read
// under the same key has not returned (a stuck mount or /proc read): then it
// starts nothing and reports false. So a stuck reading costs one parked
// goroutine, never a pile.
func (s *Sampler) launch(ctx context.Context, out chan<- reading, key, source string, fn func(context.Context) (any, error)) bool {
	s.busyMu.Lock()
	if s.busy[key] {
		s.busyMu.Unlock()
		return false
	}
	s.busy[key] = true
	s.busyMu.Unlock()
	go func() {
		v, err := fn(ctx)
		// The key is free the moment the reading returns, before its result
		// is handed over: the sample that receives the result may finish and
		// the next one start at once, and it must not find the key busy. The
		// hand-over never waits (the channel is sized for every reading of a
		// sample, one slot per key), so nothing parks here.
		s.busyMu.Lock()
		delete(s.busy, key)
		s.busyMu.Unlock()
		out <- reading{key: key, source: source, val: v, err: err}
	}()
	return true
}

// sample takes one sample. The load, memory and disk readings run at the same
// time and the sample waits for them at most ReadTimeout; one that has not
// returned by then is a timeout and stays unset, and is not started again
// until it returns. A reading that fails stays unset in the sample, is
// counted, and is logged on its first failure; a sample with no reading at
// all is not published, so the previous one ages out through [Sampler.Fresh].
func (s *Sampler) sample(ctx context.Context) {
	start := s.now()
	// The boot clock is read before the readings: a sleep inside a slow
	// reading then makes the sample older, never newer, since the reading may
	// have been taken before the sleep.
	boot, bootErr := s.o.Boot()
	t := newTally(s.m)
	r := &agentv1.ResourceSample{}
	rd := s.o.Readers

	paths, capped := s.diskPaths()
	if capped {
		t.fail(ctx, sourceDisk, errConstraint)
	}
	out := make(chan reading, 2+len(paths))
	pending := map[string]string{} // key -> source
	start1 := func(key, source string, fn func(context.Context) (any, error)) {
		if s.launch(ctx, out, key, source, fn) {
			pending[key] = source
		} else {
			t.fail(ctx, source, errBusy)
		}
	}
	start1("load", sourceLoad, func(ctx context.Context) (any, error) {
		a, b, err := rd.Load(ctx)
		return loadVal{a, b}, err
	})
	start1("mem", sourceMem, func(ctx context.Context) (any, error) {
		a, b, err := rd.Mem(ctx)
		return memVal{a, b}, err
	})
	for i, p := range paths {
		start1("disk:"+p, sourceDisk, func(ctx context.Context) (any, error) {
			f, tot, err := rd.Usage(ctx, p)
			return diskVal{i, p, f, tot}, err
		})
	}

	var total, avail uint64
	memOK := false
	disks := make([]*agentv1.DiskFree, len(paths))
	timer := time.NewTimer(s.o.ReadTimeout)
	defer timer.Stop()
wait:
	for len(pending) > 0 {
		select {
		case rr := <-out:
			delete(pending, rr.key)
			total, avail, memOK = s.accept(ctx, t, r, disks, rr, total, avail, memOK)
		case <-timer.C:
			for _, source := range pending {
				t.fail(ctx, source, context.DeadlineExceeded)
			}
			break wait
		case <-ctx.Done():
			break wait
		}
	}
	if ctx.Err() != nil {
		return // stopping: a sample cut short is neither published nor a failure
	}
	if lvl, err := rd.Pressure(ctx, total, avail, memOK); err != nil {
		t.fail(ctx, sourceMemPress, err)
	} else {
		r.MemPressureLevel = proto.Uint32(lvl)
		t.ok(sourceMemPress)
	}
	for _, d := range disks {
		if d != nil {
			r.Disk = append(r.Disk, d)
		}
	}
	t.settle(ctx)

	end := s.now()
	s.m.took(ctx, end.Sub(start))
	if r.Load1 == nil && r.MemTotalBytes == nil && r.MemPressureLevel == nil && len(r.GetDisk()) == 0 {
		return
	}
	smp := &Sample{Taken: start, Resources: r}
	if bootErr == nil {
		smp.boot, smp.bootOK = boot, true
	}
	s.publish(smp)
}

// accept records one finished reading, rejecting a value that cannot be true:
// a filesystem or memory of size 0, or free space above the total, is not a
// reading.
func (s *Sampler) accept(ctx context.Context, t *tally, r *agentv1.ResourceSample, disks []*agentv1.DiskFree, rr reading, total, avail uint64, memOK bool) (uint64, uint64, bool) {
	if rr.err != nil {
		t.fail(ctx, rr.source, rr.err)
		return total, avail, memOK
	}
	switch v := rr.val.(type) {
	case loadVal:
		r.Load1, r.Load5 = proto.Float64(v.l1), proto.Float64(v.l5)
	case memVal:
		if v.total == 0 || v.avail > v.total {
			t.fail(ctx, sourceMem, errParse)
			return total, avail, memOK
		}
		r.MemTotalBytes, r.MemAvailableBytes = proto.Uint64(v.total), proto.Uint64(v.avail)
		return v.total, v.avail, true
	case diskVal:
		if v.total == 0 || v.free > v.total {
			t.fail(ctx, sourceDisk, errParse)
			return total, avail, memOK
		}
		disks[v.i] = &agentv1.DiskFree{Path: v.path, FreeBytes: v.free, TotalBytes: v.total}
	}
	t.ok(rr.source)
	return total, avail, memOK
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
