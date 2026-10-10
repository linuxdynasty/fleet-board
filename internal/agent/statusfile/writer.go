package statusfile

import (
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

// The cadence of defaults.md, "Agent status file (status.json) rewrite":
// changes in between go into the next write, and a quiet agent still
// writes once a minute so doctor can tell a live agent from a dead one.
const (
	minInterval = time.Second
	floor       = time.Minute
	// stopWait is the longest [Writer.Stop] waits for the last write.
	stopWait = 5 * time.Second
)

// timer is the part of time.Timer the writer uses.
type timer interface {
	C() <-chan time.Time
	Stop()
}

// clock is time, replaced by a fake in tests.
type clock interface {
	Now() time.Time
	NewTimer(d time.Duration) timer
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

func (realClock) NewTimer(d time.Duration) timer { return realTimer{time.NewTimer(d)} }

type realTimer struct{ t *time.Timer }

func (r realTimer) C() <-chan time.Time { return r.t.C }
func (r realTimer) Stop()               { r.t.Stop() }

// Options are the inputs of [Start].
type Options struct {
	// Dir is the agent state folder; it must exist.
	Dir string
	// Source returns the loop's status ([conn.Loop.Status]). [Start] calls
	// it once from the caller's goroutine, before the writer's goroutine
	// runs; after that only the writer's goroutine calls it.
	Source    func() conn.Status
	Telemetry *telemetry.Telemetry
	// Changed, when set, says the status changed, like [Writer.Notify]:
	// the loop's change notification (Loop.StatusChanged, plan row 6 PR
	// 6a). A nil channel never fires. It is read until Stop.
	Changed <-chan struct{}

	// Test seams: the clock, a hook run after each write attempt, and the
	// disk steps.
	clock      clock
	afterWrite func(error)
	ops        fileOps
}

// Writer keeps status.json current. One goroutine does all the file work:
// it writes at [Start], at most once a second after a [Writer.Notify], at
// least once a minute, and once more in [Writer.Stop]. The minute starts
// when a write ends, so the real gap between two writes is a minute plus the
// time one write takes; a reader must treat the file as stale only well past
// a minute.
//
//	caller goroutines --Notify--> changed (cap 1, coalesces) --\
//	Stop() ------------close----> stop                          >-- writer goroutine --> Source(), file
//	throttle timer (armed by a change), floor timer ------------/
//
// No lock is held across a file operation: the goroutine owns lastWrite and
// the timers, Notify only does a non-blocking send, and Stop closes a
// channel once and waits for the goroutine.
type Writer struct {
	opts    Options
	clk     clock
	rec     *recorder
	log     *slog.Logger
	changed chan struct{}
	stop    chan struct{}
	done    chan struct{}
	once    sync.Once

	lastWrite time.Time // owned by the goroutine (and Start, before it runs)
}

// Start writes status.json once and starts the goroutine that keeps it
// current. A write that fails is logged and counted, not returned: a
// status file the agent cannot write never stops the agent.
func Start(opts Options) (*Writer, error) {
	switch {
	case opts.Dir == "":
		return nil, errors.New("statusfile: Start needs a folder")
	case opts.Source == nil:
		return nil, errors.New("statusfile: Start needs a status source")
	case opts.Telemetry == nil:
		return nil, errors.New("statusfile: Start needs telemetry")
	}
	if opts.clock == nil {
		opts.clock = realClock{}
	}
	m, err := newMetrics(opts.Telemetry)
	if err != nil {
		return nil, err
	}
	w := &Writer{
		opts: opts, clk: opts.clock, rec: m.recorder(StatusFile, opts.clock.Now), log: m.log,
		changed: make(chan struct{}, 1), stop: make(chan struct{}), done: make(chan struct{}),
	}
	w.write()
	go w.run()
	return w, nil
}

// Notify says the status changed. It never blocks; many calls before the
// next write make one write. After [Writer.Stop] it does nothing.
func (w *Writer) Notify() {
	select {
	case w.changed <- struct{}{}:
	default:
	}
}

// Stop writes the file once more, so it carries the loop's final state
// (the end it stopped on), and waits for the goroutine. Call it after the
// loop's Run returned, whether or not the run's context was cancelled: it
// uses no context, so a stop signal cannot cut the last write. It waits at
// most five seconds for that write, then logs status.stop_timeout and
// returns. It is safe to call more than once.
func (w *Writer) Stop() {
	w.once.Do(func() { close(w.stop) })
	wait := w.clk.NewTimer(stopWait)
	defer wait.Stop()
	select {
	case <-w.done:
	case <-wait.C():
		// A write stuck in the disk must not hold up the agent's exit (and
		// the store's close behind it). The goroutine ends when the disk
		// answers, or with the process.
		w.log.Warn("status.stop_timeout", "file", StatusFile, "wait_ms", stopWait.Milliseconds())
	}
}

func (w *Writer) run() {
	defer close(w.done)
	var (
		dirty    bool
		throttle timer
		floorT   = w.clk.NewTimer(floor)
	)
	stopTimers := func() {
		floorT.Stop()
		if throttle != nil {
			throttle.Stop()
			throttle = nil
		}
	}
	// flush writes, clears the pending change and restarts the minute.
	flush := func() {
		w.write()
		dirty = false
		floorT.Stop()
		floorT = w.clk.NewTimer(floor)
	}
	var throttleC <-chan time.Time
	// onChange writes at once when the last write was over a second ago,
	// else arms one timer for what is left of the second.
	onChange := func() {
		dirty = true
		if throttle != nil {
			return
		}
		wait := minInterval - w.clk.Now().Sub(w.lastWrite)
		if wait <= 0 {
			flush()
			return
		}
		throttle = w.clk.NewTimer(wait)
		throttleC = throttle.C()
	}
	for {
		select {
		case <-w.stop:
			stopTimers()
			w.write()
			return
		case <-w.changed:
			onChange()
		case <-w.opts.Changed: // nil until the loop's notification is wired
			onChange()
		case <-throttleC:
			throttle, throttleC = nil, nil
			if dirty {
				flush()
			}
		case <-floorT.C():
			if throttle != nil {
				throttle.Stop()
				throttle, throttleC = nil, nil
			}
			flush()
		}
	}
}

// write builds the file from the loop's status now and replaces it.
func (w *Writer) write() {
	now := w.clk.Now()
	w.lastWrite = now
	data, err := encode(FromConn(w.opts.Source(), now))
	if err == nil {
		err = writeAtomic(w.opts.Dir, StatusFile, data, w.opts.ops)
	}
	w.rec.done(err)
	if w.opts.afterWrite != nil {
		w.opts.afterWrite(err)
	}
}
