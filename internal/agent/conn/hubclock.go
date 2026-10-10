package conn

import "time"

// HubClock is the strict hub clock that command deadlines are checked on
// (02i "Clocks"). It is kept apart from the lenient clocks the loop uses
// for pruning (session.hubNow) and renewal (Loop.hubTime), which may run
// late but never expire a command.
type HubClock interface {
	// Now is the latest HelloAck's hub_time plus the time elapsed since
	// it arrived, the larger of the monotonic and the sleep-counting
	// clocks. ok is false when hub time is not known: before the first
	// HelloAck of the run, after a HelloAck with no hub_time, or when the
	// sleep-counting clock could not be read at that HelloAck or now. A
	// deadline checked while ok is false is past: the command is EXPIRED
	// and never started.
	Now() (t time.Time, ok bool)
}

// HubClock returns the loop's strict hub clock. It is safe to read from
// any goroutine; each HelloAck refines it.
func (l *Loop) HubClock() HubClock { return strictClock{l: l} }

type strictClock struct{ l *Loop }

func (c strictClock) Now() (time.Time, bool) {
	l := c.l
	a := l.anchor.Load()
	if a == nil || !a.sleepOK {
		return time.Time{}, false
	}
	d, err := l.sleepClock()
	if err != nil {
		return time.Time{}, false
	}
	return a.at.Add(max(l.since(a.recv), d-a.sleep)), true
}
