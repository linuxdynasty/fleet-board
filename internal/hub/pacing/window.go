package pacing

import (
	"time"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

const (
	// defaultWeeklyWindow is assumed for weekly buckets without a window. A
	// monthly bucket never assumes one (03b, "Monthly windows").
	defaultWeeklyWindow = 7 * 24 * time.Hour
	// seenOverrideTolerance is how far the hub-seen window may drift from
	// LimitBucket.window before the seen one wins (03b, "Monthly windows").
	seenOverrideTolerance = time.Hour
)

// WindowSource says which window the deciding bucket's line used. It feeds
// the window_source field of the pacing.decision log the store slice writes.
type WindowSource int

const (
	// WindowNone means no line was drawn (unknown, stale or excluded).
	WindowNone WindowSource = iota
	// WindowSevenDayDefault is the assumed 7 days for a weekly bucket with
	// no window.
	WindowSevenDayDefault
	// WindowReading is LimitBucket.window from the reading.
	WindowReading
	// WindowSeenResets is the window the hub saw between two resets.
	WindowSeenResets
	// WindowSeenOverridesReading means both were set and differed by more
	// than an hour, so the seen window won.
	WindowSeenOverridesReading
)

// String keeps the log value stable.
func (s WindowSource) String() string {
	switch s {
	case WindowSevenDayDefault:
		return "seven_day_default"
	case WindowReading:
		return "reading"
	case WindowSeenResets:
		return "seen_resets"
	case WindowSeenOverridesReading:
		return "seen_overrides_reading"
	default:
		return "none"
	}
}

// decided is one bucket's line values.
type decided struct {
	b      *typev1.LimitBucket
	used   float64
	resets time.Time
	start  time.Time
	window time.Duration
	src    WindowSource
	ahead  float64
}

// lineBucket resolves one named bucket for the projection on early-return
// states (the reserve alert still carries the waste line).
func lineBucket(now time.Time, needed []*typev1.LimitBucket, key string, in Input) (decided, bool) {
	for _, b := range needed {
		if b.GetKey() != key {
			continue
		}
		if d, ok := bucketLine(now, b, in); ok {
			return d, true
		}
		return decided{}, false
	}
	return decided{}, false
}

// decideLineBucket returns the bucket the burn-down line would use, for the
// reading_stale flag on early-return states.
func decideLineBucket(now time.Time, u *hubv1.AccountUsage, in Input) (*typev1.LimitBucket, bool) {
	needed := neededBuckets(u.GetBuckets(), in.GoalModel)
	d, ok := decideLine(now, needed, in)
	if !ok {
		return nil, false
	}
	return d.b, true
}

// decideLine picks the needed bucket with the largest ahead. A bucket
// without a usable window is skipped; the reserve line still applies to its
// known percent, which the caller checks before reaching here.
func decideLine(now time.Time, needed []*typev1.LimitBucket, in Input) (decided, bool) {
	var best decided
	found := false
	for _, b := range needed {
		d, ok := bucketLine(now, b, in)
		if !ok {
			continue
		}
		if !found || d.ahead > best.ahead {
			best, found = d, true
		}
	}
	return best, found
}

// bucketLine draws one bucket's line. It reports false when the bucket has
// no known percent, no resolved reset, or no usable window.
func bucketLine(now time.Time, b *typev1.LimitBucket, in Input) (decided, bool) {
	used, ok := percentValue(b.GetPercentUsed())
	if !ok {
		return decided{}, false
	}
	resets := timestampTime(b.GetResetsTime().GetResolvedTime())
	if resets.IsZero() {
		return decided{}, false
	}
	window, src, ok := resolveWindow(b, in)
	if !ok {
		return decided{}, false
	}
	limit := float64(100 - reserve(in.ReservePercent))
	start := resets.Add(-window)
	elapsed := now.Sub(start)
	if elapsed < 0 {
		elapsed = 0
	}
	target := float64(elapsed) / float64(window) * limit
	return decided{b: b, used: used, resets: resets, start: start, window: window, src: src, ahead: used - target}, true
}

// resolveWindow picks a monthly bucket's window (hub-seen, then the reading,
// else unknown) or a weekly bucket's window (the reading, else 7 days). A
// short bucket never gets a line.
func resolveWindow(b *typev1.LimitBucket, in Input) (time.Duration, WindowSource, bool) {
	if b.GetKind() == typev1.WindowKind_WINDOW_KIND_MONTHLY {
		if in.HaveSeenWindow && in.SeenWindow > 0 {
			if w := bucketWindow(b); w > 0 && absDuration(w-in.SeenWindow) > seenOverrideTolerance {
				return in.SeenWindow, WindowSeenOverridesReading, true
			}
			return in.SeenWindow, WindowSeenResets, true
		}
		if w := bucketWindow(b); w > 0 {
			return w, WindowReading, true
		}
		return 0, WindowNone, false
	}
	if w := bucketWindow(b); w > 0 {
		return w, WindowReading, true
	}
	return defaultWeeklyWindow, WindowSevenDayDefault, true
}

// bucketWindow returns the reading's window, or 0 when unset.
func bucketWindow(b *typev1.LimitBucket) time.Duration {
	if b.GetWindow() == nil {
		return 0
	}
	return b.GetWindow().AsDuration()
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}
