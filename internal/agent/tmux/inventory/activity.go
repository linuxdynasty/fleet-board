package inventory

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux"
)

// defaultIdleThreshold is the output-quiet edge of E4 activity when the
// observer's options name none (defaults.md `idle_threshold`).
const defaultIdleThreshold = 2 * time.Minute

// summaryGap is the least time between two E5 summaries of one pane
// (01 E4-E5: at most one per pane every 5 s).
const summaryGap = 5 * time.Second

// activitySub is the one window-activity subscription per server the row-7
// ruling asks for: tmux checks it about once a second and sends
// %subscription-changed when any window's activity time moves. Its value
// is never parsed (any change only dirties the activity read); the
// window-activity read below carries the times.
var activitySub = tmux.Subscription{
	Name:   "fleet-activity",
	Format: "#{S:#{session_id}:#{W:#{window_id}=#{window_activity} }}",
}

// paneOutlet is one live pane an activity read reports on: its window's
// activity time (epoch, from tmux) and whether tmux kept it dead.
type paneOutlet struct {
	pane       string // "%N"
	window     string // "@N"
	lastWindow time.Time
	dead       bool
}

// activityChange is one pane's E4 edge.
type activityChange struct {
	pane     string
	from, to typev1.Activity
	lastOut  time.Time
}

// activityTracker keeps E4 state for one server. It is pure: the sync loop
// owns it, feeds one read's panes through evaluate, and emits what evaluate
// returns. The only clock is the window-activity time tmux reports (row-7
// ruling: %output is not a source). A pane seen first is
// initialized silently (the connect Snapshot carries the level, as with
// E1/E3); a dead or gone pane is forgotten without an edge, so a respawn
// starts silent too.
type activityTracker struct {
	now   func() time.Time
	idle  time.Duration
	last  map[string]time.Time
	state map[string]typev1.Activity
}

func newActivityTracker(now func() time.Time, idle time.Duration) *activityTracker {
	if idle <= 0 {
		idle = defaultIdleThreshold
	}
	return &activityTracker{
		now:   now,
		idle:  idle,
		last:  map[string]time.Time{},
		state: map[string]typev1.Activity{},
	}
}

// evaluate folds one read's panes into the state and returns its edges.
// lastWindow is the pane's window activity time, the pane's last output.
func (t *activityTracker) evaluate(panes []paneOutlet) []activityChange {
	now := t.now()
	seen := make(map[string]bool, len(panes))
	var changes []activityChange
	for _, p := range panes {
		if p.dead {
			delete(t.state, p.pane)
			delete(t.last, p.pane)
			continue
		}
		seen[p.pane] = true
		last := p.lastWindow
		t.last[p.pane] = last
		to := typev1.Activity_ACTIVITY_IDLE
		if now.Sub(last) < t.idle {
			to = typev1.Activity_ACTIVITY_ACTIVE
		}
		from, ok := t.state[p.pane]
		if !ok {
			t.state[p.pane] = to
			continue
		}
		if from != to {
			t.state[p.pane] = to
			changes = append(changes, activityChange{pane: p.pane, from: from, to: to, lastOut: last})
		}
	}
	for pane := range t.state {
		if !seen[pane] {
			delete(t.state, pane)
			delete(t.last, pane)
		}
	}
	return changes
}

// nextIdle is when the oldest active pane goes quiet, for the loop's idle
// timer: with no new output the timer's evaluate emits the idle edges, so
// a server gone quiet still reports them (the hub's stuck detection
// depends on E4, and no notice arrives to wake the loop).
func (t *activityTracker) nextIdle() (time.Time, bool) {
	var at time.Time
	ok := false
	for pane, st := range t.state {
		if st != typev1.Activity_ACTIVITY_ACTIVE {
			continue
		}
		if idleAt := t.last[pane].Add(t.idle); !ok || idleAt.Before(at) {
			at, ok = idleAt, true
		}
	}
	return at, ok
}

// stamp sets each pane's activity and last-output time from the tracked
// state: the Snapshot and every event carrying a pane show the level (01
// E4). A pane the tracker never saw keeps its zero value (unknown), which
// is what a read before the first activity refresh reports.
func (t *activityTracker) stamp(srv *agentv1.TmuxServer) {
	for _, s := range srv.GetSessions() {
		for _, w := range s.GetWindows() {
			for _, p := range w.GetPanes() {
				id := p.GetKey().GetPaneId()
				if st, ok := t.state[id]; ok {
					p.Activity = st
				}
				if last, ok := t.last[id]; ok {
					p.LastOutputTime = timestamppb.New(last)
				}
			}
		}
	}
}

// reset forgets every pane: a server restart reuses no state (tmux ids
// restart from zero with a new server).
func (t *activityTracker) reset() {
	t.last = map[string]time.Time{}
	t.state = map[string]typev1.Activity{}
}

// dueSummary reports whether an E5 summary may go out for pane now: the
// first one always may, then at most one per summaryGap.
func dueSummary(last map[string]time.Time, pane string, now time.Time) bool {
	prev, ok := last[pane]
	return !ok || now.Sub(prev) >= summaryGap
}

// refreshActivity brings the window-activity times up to date when the
// subscription moved (or never read). A failed activity read keeps the
// cached times and is logged.
func (s *server) refreshActivity(ctx context.Context, force bool) {
	dirty := force || len(s.winAct) == 0
	select {
	case <-s.actDirty:
		dirty = true
	default:
	}
	if dirty {
		rctx, cancel := context.WithTimeout(ctx, readTimeout)
		act, err := s.c.ReadWindowActivity(rctx)
		cancel()
		switch {
		case err == nil:
			win := make(map[string]time.Time, len(act))
			for id, at := range act {
				win[id.String()] = at
			}
			s.winAct = win
		case ctx.Err() == nil && !errors.Is(err, tmux.ErrNotAttached):
			s.o.tel.activityFailed(ctx, s.kind, err)
		}
	}
}

// outlets turns one inventory read into the tracker's input: each pane with
// its window's activity time.
func (s *server) outlets(inv tmux.Inventory) []paneOutlet {
	out := make([]paneOutlet, 0, len(inv.Panes))
	for _, p := range inv.Panes {
		out = append(out, paneOutlet{pane: p.ID.String(), window: p.Window.String(), lastWindow: s.winAct[p.Window.String()], dead: p.Dead})
	}
	return out
}

// outletsFromLevel turns the stored inventory into the tracker's input, for
// evaluations with no fresh read (activity notices, the idle timer).
func (s *server) outletsFromLevel() []paneOutlet {
	var out []paneOutlet
	for _, sess := range s.last.GetSessions() {
		for _, w := range sess.GetWindows() {
			for _, p := range w.GetPanes() {
				out = append(out, paneOutlet{
					pane: p.GetKey().GetPaneId(), window: p.GetWindow().GetWindowId(),
					lastWindow: s.winAct[p.GetWindow().GetWindowId()], dead: p.GetDead(),
				})
			}
		}
	}
	return out
}

// syncActivity re-evaluates activity after a window-activity notice, with no
// inventory read: it refreshes the window times, emits the edges, and
// re-stamps the stored level so the Snapshot between reads stays fresh.
func (s *server) syncActivity(ctx context.Context) {
	if s.c.State() != tmux.StateUp || s.last == nil {
		return
	}
	s.refreshActivity(ctx, true)
	s.track.stamp(s.last)
	s.emitChanges(ctx, s.last.GetServerStartId(), s.track.evaluate(s.outletsFromLevel()))
}

// reapIdle re-evaluates from the cached times when the idle timer fires: a
// server gone quiet emits its idle edges with no new notice (E4 is never
// dropped, and the hub's stuck detection depends on it).
func (s *server) reapIdle(ctx context.Context) {
	if s.c.State() != tmux.StateUp || s.last == nil {
		return
	}
	s.emitChanges(ctx, s.last.GetServerStartId(), s.track.evaluate(s.outletsFromLevel()))
	s.track.stamp(s.last)
}

// emitChanges emits one E4 edge per change (never dropped) and, on each
// edge, at most one E5 summary per pane per summaryGap: the visible screen
// rendered to capped, ANSI-stripped text.
func (s *server) emitChanges(ctx context.Context, start string, changes []activityChange) {
	now := s.track.now()
	for _, ch := range changes {
		edge := "to_active"
		if ch.to == typev1.Activity_ACTIVITY_IDLE {
			edge = "to_idle"
		}
		s.o.tel.transition(ctx, s.kind, edge)
		key := &typev1.PaneKey{Server: s.name, ServerStartId: start, PaneId: ch.pane}
		s.emit(ctx, &agentv1.Event{Body: &agentv1.Event_Activity{Activity: &agentv1.ActivityTransition{
			Pane: key, From: ch.from, To: ch.to, LastOutputTime: timestamppb.New(ch.lastOut),
		}}})
		if s.o.opts.Emitter == nil || !dueSummary(s.summaries, ch.pane, now) {
			continue
		}
		if s.summarize(ctx, key, ch) {
			s.summaries[ch.pane] = now
		}
	}
}

// summarize captures one pane's visible screen and emits it as an E5
// summary. It reports whether the summary went out; a failed capture is
// counted and the edge keeps its transition without one. The capture is
// bounded at readTimeout like every other observer read: a server that
// stops answering mid-sync (#208) must not hold the loop past it, or no
// further read runs and snapshots stop being served.
func (s *server) summarize(ctx context.Context, key *typev1.PaneKey, ch activityChange) bool {
	id, ok := parsePaneID(ch.pane)
	if !ok {
		s.o.tel.summary(ctx, s.kind, "capture_failed")
		return false
	}
	rctx, cancel := context.WithTimeout(ctx, readTimeout)
	cap, err := s.c.Capture(rctx, id)
	cancel()
	if err != nil {
		s.o.tel.summary(ctx, s.kind, "capture_failed")
		return false
	}
	text, _ := renderScreen(cap.Lines, false, maxSummaryBytes)
	s.emit(ctx, &agentv1.Event{Body: &agentv1.Event_PaneSummary{PaneSummary: &agentv1.PaneSummary{
		Pane: key, LastLines: text, LastOutputTime: timestamppb.New(ch.lastOut), Activity: ch.to,
	}}})
	s.o.tel.summary(ctx, s.kind, "ok")
	return true
}

// parsePaneID parses a "%N" pane id the read reported; anything else is not
// a pane and is never sent to tmux.
func parsePaneID(s string) (tmux.PaneID, bool) {
	n, err := strconv.Atoi(strings.TrimPrefix(s, "%"))
	if err != nil || n < 0 || tmux.PaneID(n).String() != s {
		return 0, false
	}
	return tmux.PaneID(n), true
}
