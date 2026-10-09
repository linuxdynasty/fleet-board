package main

import "time"

// SleepMark is the kernel's own record of sleep, read by method (s).
// macOS sets WakeTime (sysctl kern.waketime; zero until the first wake since
// boot). Linux sets Suspended (CLOCK_BOOTTIME minus CLOCK_MONOTONIC, the total
// time spent suspended since boot).
type SleepMark struct {
	WakeTime  time.Time
	Suspended time.Duration
}

// Wake is one detected wake. At is the kernel's wake time when known (macOS);
// Slept is how long the machine was suspended when known (Linux).
type Wake struct {
	At    time.Time
	Slept time.Duration
}

// SleepMarkDetector reports a wake when the kernel's sleep record changes.
type SleepMarkDetector struct {
	Tolerance time.Duration // Linux: growth in Suspended below this is drift
	prev      SleepMark
	started   bool
}

// Observe takes one reading and reports whether a wake happened since the last.
func (d *SleepMarkDetector) Observe(m SleepMark) (Wake, bool) {
	if !d.started {
		d.prev, d.started = m, true
		return Wake{}, false
	}
	prev := d.prev
	d.prev = m
	if !m.WakeTime.IsZero() && !m.WakeTime.Equal(prev.WakeTime) {
		return Wake{At: m.WakeTime}, true
	}
	if grew := m.Suspended - prev.Suspended; grew > d.Tolerance {
		return Wake{Slept: grew}, true
	}
	return Wake{}, false
}
