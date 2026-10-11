package exec

import (
	"context"
	"slices"
	"time"
)

// DefaultRateLimit is the per-kind command limit when neither policy.yaml
// nor the hub states one (defaults.md "Agent command rate limit").
const DefaultRateLimit = 60

// rateWindow counts every logged command_id of a kind in the last minute,
// logged refusals included (ruling fork 4 i), each id once. Only the
// envelope goroutine uses it. Run rebuilds it from the command log before
// it takes a command, so a restart does not reset the limit (6e ruling,
// fork 8). Its times and received_time are the agent's clock, which can
// jump: a stamp later than now (the clock went back, or an entry logged
// while it ran ahead) counts as now, so the window still frees within a
// minute of the clock as it reads now, and a wait is never negative.
type rateWindow struct {
	times map[string][]stamp // made in New; never nil
}

// stamp is one counted command: when it was logged, and its command_id
// ("" for an entry seeded from the log, which is counted by its time).
type stamp struct {
	at time.Time
	id string
}

const rateSpan = time.Minute

// add counts one logged command of kind at at (no later than now).
func (w *rateWindow) add(kind, id string, at, now time.Time) {
	w.times[kind] = append(w.recent(kind, now), stamp{at: notAfter(at, now), id: id})
}

// recent keeps what is in the window, (now-1m, now], whatever its order:
// a stamp later than now is moved to now, and one at or before now-1m is
// dropped. It returns the stamps oldest first.
func (w *rateWindow) recent(kind string, now time.Time) []stamp {
	kept := w.times[kind][:0]
	for _, s := range w.times[kind] {
		s.at = notAfter(s.at, now)
		if s.at.After(now.Add(-rateSpan)) {
			kept = append(kept, s)
		}
	}
	slices.SortStableFunc(kept, func(a, b stamp) int { return a.at.Compare(b.at) })
	w.times[kind] = kept
	return kept
}

// notAfter is t, or now when t is later.
func notAfter(t, now time.Time) time.Time {
	if t.After(now) {
		return now
	}
	return t
}

// counts reports whether id is already in kind's window.
func (w *rateWindow) counts(kind, id string, now time.Time) bool {
	return id != "" && slices.ContainsFunc(w.recent(kind, now), func(s stamp) bool { return s.id == id })
}

// overCounting reports whether kind holds more than limit commands in the
// window, the one being checked (id, logged at at) included once, and when
// a new one would fit: the window counts refusals, so it frees as the
// newest ones over the limit age out. Inside the window the entry is
// already counted. Older (a resumed entry from before the window) it is
// added now, as a fresh command is when it is logged, so the next entry of
// a backlog counts it, but only once per command_id: an entry whose result
// write keeps failing is taken up again at every connect and must not
// count itself again each time.
func (w *rateWindow) overCounting(kind, id string, now, at time.Time, limit int) (time.Duration, bool) {
	if !at.After(now.Add(-rateSpan)) && !w.counts(kind, id, now) {
		w.add(kind, id, now, now)
	}
	ts := w.recent(kind, now)
	if limit < 1 || len(ts) <= limit {
		return 0, false
	}
	return max(0, ts[len(ts)-limit].at.Add(rateSpan).Sub(now)), true
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
		e.window.add(l.Kind, "", l.Received, now)
	}
}
