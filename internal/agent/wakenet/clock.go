package wakenet

import "time"

// Stamp is one reading of the wall clock and of Go's monotonic clock. Mono is
// time since a fixed start; Go's monotonic clock stops while the machine
// sleeps, the wall clock does not.
type Stamp struct {
	Wall time.Time
	Mono time.Duration
}

// Clock is the time seam: tests move it by hand.
type Clock interface {
	Now() Stamp
	// After fires once, d of monotonic time from now.
	After(d time.Duration) <-chan time.Time
}

// SystemClock returns the real clock. Wall has its monotonic part stripped,
// so subtracting two Walls measures the wall clock only.
func SystemClock() Clock { return sysClock{start: time.Now()} }

type sysClock struct{ start time.Time }

func (c sysClock) Now() Stamp {
	t := time.Now()
	return Stamp{Wall: t.Round(0), Mono: t.Sub(c.start)}
}

func (sysClock) After(d time.Duration) <-chan time.Time { return time.After(d) }
