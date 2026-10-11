package exec

import (
	"context"
	"time"
)

// DefaultRateLimit is the per-kind command limit when neither policy.yaml
// nor the hub states one (defaults.md "Agent command rate limit").
const DefaultRateLimit = 60

// rateWindow counts every logged command_id of a kind in the last minute,
// logged refusals included (ruling fork 4 i). Only the envelope goroutine
// uses it. Run rebuilds it from the command log before it takes a command,
// so a restart does not reset the limit (6e ruling, fork 8). Both it and
// received_time are the agent's clock; a clock jump moves it by at most a
// minute.
type rateWindow struct {
	times map[string][]time.Time
}

const rateSpan = time.Minute

// add counts one logged command of kind at now.
func (w *rateWindow) add(kind string, now time.Time) {
	if w.times == nil {
		w.times = map[string][]time.Time{}
	}
	w.times[kind] = append(w.recent(kind, now), now)
}

// recent drops what left the window and returns what is in it, oldest first.
func (w *rateWindow) recent(kind string, now time.Time) []time.Time {
	ts := w.times[kind]
	i := 0
	for i < len(ts) && !ts[i].After(now.Add(-rateSpan)) {
		i++
	}
	ts = ts[i:]
	w.times[kind] = ts
	return ts
}

// overCounting reports whether kind holds more than limit commands in the
// window, the one being checked included once, and when a new one would
// fit: the window counts refusals, so it frees as the newest ones over the
// limit age out. at is when the checked command was logged: inside the
// window it is already counted; older, it counts as if logged now.
func (w *rateWindow) overCounting(kind string, now, at time.Time, limit int) (time.Duration, bool) {
	ts := w.recent(kind, now)
	if !at.After(now.Add(-rateSpan)) {
		ts = append(append([]time.Time(nil), ts...), now)
	}
	if limit < 1 || len(ts) <= limit {
		return 0, false
	}
	return ts[len(ts)-limit].Add(rateSpan).Sub(now), true
}

// seedWindow rebuilds the window from the last minute of the command log.
// A read that fails is logged and the window starts empty: the limit
// fails open, it never stops the executor.
func (e *Executor) seedWindow(ctx context.Context) {
	now := e.opts.Now()
	recent, err := e.opts.DB.Recent(ctx, now.Add(-rateSpan))
	if err != nil {
		e.storeFailed(ctx, "", "window", err)
		return
	}
	for _, l := range recent {
		e.window.add(l.Kind, l.Received)
	}
}
