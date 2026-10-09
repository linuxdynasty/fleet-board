package wakenet

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
)

const (
	// wakeCheckEvery is how often the wake check runs (defaults.md, Wake check).
	wakeCheckEvery = 5 * time.Second
	// wakeThreshold is how much either signal must grow in one check to be a wake.
	wakeThreshold = 2 * time.Second
	// asleepJitter is how far below zero a time-asleep reading may fall and
	// still count as zero: the two clocks are read one after the other.
	asleepJitter = time.Second
)

// Wake is the wake watcher. Every 5 s it reads the time asleep since boot and
// compares how far the wall clock moved with how far Go's monotonic clock
// moved; either growing by 2 s or more is one wake.
type Wake struct {
	Clock Clock
	// Asleep returns the time asleep since boot; see [SystemAsleep].
	Asleep func() (time.Duration, error)
	Log    *slog.Logger
}

// NewWake returns a wake watcher on the system clocks.
func NewWake(log *slog.Logger) *Wake {
	return &Wake{Clock: SystemClock(), Asleep: SystemAsleep, Log: log}
}

var errAsleepNegative = errors.New("time asleep since boot is negative")

// Run checks until ctx ends and sends one Event per wake to out.
func (w *Wake) Run(ctx context.Context, out chan<- Event) {
	var st wakeState
	st.observe(ctx, w, w.Clock.Now())
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.Clock.After(wakeCheckEvery):
		}
		info, woke := st.observe(ctx, w, w.Clock.Now())
		if !woke {
			continue
		}
		w.Log.InfoContext(ctx, "wake.detected",
			"signal", info.signal(),
			"asleep_delta_ms", info.AsleepDelta.Milliseconds(),
			"wall_gap_ms", info.WallGap.Milliseconds())
		select {
		case out <- Event{Reason: agentv1.ConnectReason_CONNECT_REASON_WAKE, Wake: info}:
		case <-ctx.Done():
			return
		}
	}
}

// wakeState is the last good reading of each signal.
type wakeState struct {
	prev       Stamp
	havePrev   bool
	asleep     time.Duration
	haveAsleep bool
	failures   int // time-asleep reads failed in a row
}

// observe takes one check's readings and reports whether either signal grew
// by the threshold. A failed time-asleep read keeps the last good reading, so
// sleep across a failed check is still counted on the next one. The first of a
// run of failures is logged, and the recovery with the count.
func (s *wakeState) observe(ctx context.Context, w *Wake, now Stamp) (WakeInfo, bool) {
	var info WakeInfo
	if s.havePrev {
		info.WallGap = now.Wall.Sub(s.prev.Wall) - (now.Mono - s.prev.Mono)
		switch {
		case info.WallGap >= wakeThreshold:
			info.WallAhead = true
		case info.WallGap <= -wakeThreshold:
			w.Log.InfoContext(ctx, "wake.wall_clock_back", "wall_gap_ms", info.WallGap.Milliseconds())
		}
		if !info.WallAhead {
			info.WallGap = 0
		}
	}
	s.prev, s.havePrev = now, true

	if asleep, err := readAsleep(w.Asleep); err != nil {
		s.failures++
		if s.failures == 1 {
			w.Log.WarnContext(ctx, "wake.clock_read_failed", "err", err.Error())
		}
	} else {
		if s.failures > 0 {
			w.Log.InfoContext(ctx, "wake.clock_read_recovered", "failures", s.failures)
			s.failures = 0
		}
		if s.haveAsleep && asleep-s.asleep >= wakeThreshold {
			info.AsleepGrew, info.AsleepDelta = true, asleep-s.asleep
		}
		s.asleep, s.haveAsleep = asleep, true
	}
	return info, info.AsleepGrew || info.WallAhead
}

// readAsleep reads the time asleep and clamps the small negative values that
// reading two clocks one after the other can give.
func readAsleep(read func() (time.Duration, error)) (time.Duration, error) {
	d, err := read()
	switch {
	case err != nil:
		return 0, err
	case d <= -asleepJitter:
		return 0, fmt.Errorf("%w: %v", errAsleepNegative, d)
	case d < 0:
		return 0, nil
	}
	return d, nil
}

// signal names the signals that fired, for the log line.
func (i WakeInfo) signal() string {
	switch {
	case i.AsleepGrew && i.WallAhead:
		return "both"
	case i.AsleepGrew:
		return "asleep"
	default:
		return "wall_clock"
	}
}
