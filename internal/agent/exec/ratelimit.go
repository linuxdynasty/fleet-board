package exec

import "time"

// DefaultRateLimit is the per-kind command limit when neither policy.yaml
// nor the hub states one (defaults.md "Agent command rate limit").
const DefaultRateLimit = 60

// rateWindow counts every logged command_id of a kind in the last minute,
// logged refusals included (ruling fork 4 i). Only the envelope goroutine
// uses it. It lives in memory: a restart starts it empty.
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

// over reports whether kind holds more than limit commands in the window,
// the one just added included, and when a new one would fit: the window
// counts refusals, so it frees as the newest ones over the limit age out.
func (w *rateWindow) over(kind string, now time.Time, limit int) (time.Duration, bool) {
	ts := w.recent(kind, now)
	if limit < 1 || len(ts) <= limit {
		return 0, false
	}
	return ts[len(ts)-limit].Add(rateSpan).Sub(now), true
}
