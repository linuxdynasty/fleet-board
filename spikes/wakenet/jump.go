package main

import "time"

// Jump is the progress of the wall clock and the monotonic clock between two
// readings. Gap is wall minus monotonic: positive when the machine slept (Go's
// monotonic clock stops during sleep on macOS and Linux), negative when the
// wall clock was stepped back.
type Jump struct {
	WallDelta time.Duration
	MonoDelta time.Duration
	Gap       time.Duration
}

// JumpDetector is method (a): it compares wall and monotonic progress between
// readings and reports a jump when they differ by more than Threshold.
type JumpDetector struct {
	Threshold time.Duration
	prevWall  time.Time
	prevMono  time.Duration
	started   bool
}

// Observe takes a wall reading stripped of its monotonic part (Round(0)) and
// the monotonic time elapsed since a fixed start.
func (d *JumpDetector) Observe(wall time.Time, mono time.Duration) (Jump, bool) {
	if !d.started {
		d.prevWall, d.prevMono, d.started = wall, mono, true
		return Jump{}, false
	}
	j := Jump{WallDelta: wall.Sub(d.prevWall), MonoDelta: mono - d.prevMono}
	j.Gap = j.WallDelta - j.MonoDelta
	d.prevWall, d.prevMono = wall, mono
	return j, j.Gap > d.Threshold || j.Gap < -d.Threshold
}
