package statusfile

import (
	"errors"
	"os"
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
	// Source returns the loop's status ([conn.Loop.Status]). It is called
	// from the writer's goroutine only.
	Source    func() conn.Status
	Telemetry *telemetry.Telemetry

	// Test seams: the clock, a hook run after each write attempt, and the
	// rename step.
	clock      clock
	afterWrite func(error)
	rename     func(oldpath, newpath string) error
}

// Writer keeps status.json current. One goroutine does all the file work:
// it writes at [Start], at most once a second after a [Writer.Notify], at
// least once a minute, and once more in [Writer.Stop].
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
	if opts.rename == nil {
		opts.rename = os.Rename
	}
	m, err := newMetrics(opts.Telemetry)
	if err != nil {
		return nil, err
	}
	w := &Writer{
		opts: opts, clk: opts.clock, rec: m.recorder(StatusFile, opts.clock.Now),
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
// uses no context, so a stop signal cannot cut the last write. It is safe
// to call more than once.
func (w *Writer) Stop() {
	w.once.Do(func() { close(w.stop) })
	<-w.done
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
	for {
		select {
		case <-w.stop:
			stopTimers()
			w.write()
			return
		case <-w.changed:
			dirty = true
			if throttle != nil {
				continue
			}
			wait := minInterval - w.clk.Now().Sub(w.lastWrite)
			if wait <= 0 {
				flush()
				continue
			}
			throttle = w.clk.NewTimer(wait)
			throttleC = throttle.C()
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
		err = writeAtomic(w.opts.Dir, StatusFile, data, w.opts.rename)
	}
	w.rec.done(err)
	if w.opts.afterWrite != nil {
		w.opts.afterWrite(err)
	}
}
