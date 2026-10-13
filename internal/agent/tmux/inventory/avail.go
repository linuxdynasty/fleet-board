package inventory

import (
	"fmt"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// availMark is one pane's requested availability: the developer's mark
// through the board (row 18's SetPaneAvailability drives it; this package
// only reports it). since is when the mark was recorded.
type availMark struct {
	available bool
	fresh     bool
	reason    typev1.AvailabilityReason
	since     time.Time
}

// availOverlay is the E11 scaffold: the requested marks per pane id and the
// last stamped state per pane id. It is pure: the server's loop owns it and
// feeds the live pane ids through reconcile. Idle is pinned false (row-7
// ruling fork 9): rows 8 and 18 own the idle computation and the marks'
// drivers, so this package never reports a pane idle.
type availOverlay struct {
	want  map[string]availMark
	shown map[string]availMark
}

func newAvailOverlay() *availOverlay {
	return &availOverlay{want: map[string]availMark{}, shown: map[string]availMark{}}
}

// set records a mark request; clear records its removal with the reason the
// edge carries. Neither emits: the loop's reconcile does.
func (a *availOverlay) set(id string, m availMark) { a.want[id] = m }

func (a *availOverlay) clear(id string, reason typev1.AvailabilityReason) {
	a.want[id] = availMark{reason: reason}
}

// reconcile folds the live pane ids into the state and returns the
// AvailabilityChanged edges. A mark for a pane that is gone is dropped: with
// PANE_CHANGED when it was shown available (the hub must not keep treating
// a dead pane as free capacity), silently when it never showed. sess maps a
// pane id to its fleet session id, "" when the pane has none.
func (a *availOverlay) reconcile(live map[string]bool, sess func(string) string, server, start string) []*agentv1.Event {
	var edges []*agentv1.Event
	for id, w := range a.want {
		s, ok := a.shown[id]
		switch {
		case !live[id]:
			delete(a.want, id)
			if ok {
				delete(a.shown, id)
				edges = append(edges, availEvent(server, start, id, sess(id), s,
					availMark{}, typev1.AvailabilityReason_AVAILABILITY_REASON_PANE_CHANGED))
			}
		case w.available && (!ok || s != w):
			a.shown[id] = w
			edges = append(edges, availEvent(server, start, id, sess(id), s, w, w.reason))
		case !w.available && ok:
			delete(a.want, id)
			delete(a.shown, id)
			edges = append(edges, availEvent(server, start, id, sess(id), s,
				availMark{}, w.reason))
		case !w.available:
			delete(a.want, id)
		}
	}
	return edges
}

// stamp writes each pane's Availability: idle always false, the mark's
// available, fresh-context choice and mark time when marked.
func (a *availOverlay) stamp(srv *agentv1.TmuxServer) {
	if srv == nil {
		return
	}
	for _, s := range srv.GetSessions() {
		for _, w := range s.GetWindows() {
			for _, p := range w.GetPanes() {
				p.Availability = &typev1.Availability{}
				if m, ok := a.want[p.GetKey().GetPaneId()]; ok && m.available {
					p.Availability.AvailableForWork = true
					p.Availability.FreshContextOnReuse = m.fresh
					p.Availability.SinceTime = timestamppb.New(m.since)
				}
			}
		}
	}
}

// reset forgets every mark: a server restart reuses no pane id (tmux ids
// restart from zero with a new server).
func (a *availOverlay) reset() {
	a.want = map[string]availMark{}
	a.shown = map[string]availMark{}
}

// availLive is the server's live pane ids and their fleet session ids (from
// each pane's tags, "" when the pane has none): the loop feeds both to
// reconcile after every read.
func availLive(srv *agentv1.TmuxServer) (map[string]bool, func(string) string) {
	live := map[string]bool{}
	sess := map[string]string{}
	if srv == nil {
		return live, func(string) string { return "" }
	}
	for _, s := range srv.GetSessions() {
		for _, w := range s.GetWindows() {
			for _, p := range w.GetPanes() {
				id := p.GetKey().GetPaneId()
				live[id] = true
				if v, ok := p.GetFleetTags()[tagSessionID]; ok {
					if _, seen := sess[id]; !seen {
						sess[id] = v
					}
				}
			}
		}
	}
	return live, func(id string) string { return sess[id] }
}

// ApplyAvailability records the developer's mark for one pane: available
// for work with its fresh-context choice, or not (row 18's
// SetPaneAvailability drives it; this package only reports it). The loop's
// next read reports it in the Snapshot and emits AvailabilityChanged; idle
// stays false until row 8 (row-7 ruling fork 9). It runs on the caller's
// goroutine and never reads tmux or emits: a mark for a pane the last read
// does not know, an unknown server, a stale server start or a missing
// reason is refused, so a reused pane id never inherits a mark.
func (o *Observer) ApplyAvailability(pane *typev1.PaneKey, avail *typev1.Availability, reason typev1.AvailabilityReason) error {
	if pane == nil || avail == nil {
		return fmt.Errorf("inventory: an availability mark needs a pane and a value")
	}
	if reason == typev1.AvailabilityReason_AVAILABILITY_REASON_UNSPECIFIED {
		return fmt.Errorf("inventory: an availability mark needs a reason")
	}
	s := o.servers[pane.GetServer()]
	if s == nil {
		return fmt.Errorf("inventory: unknown server %q", pane.GetServer())
	}
	s.availMu.Lock()
	defer s.availMu.Unlock()
	if s.availStart == "" || s.availStart != pane.GetServerStartId() {
		return fmt.Errorf("inventory: server %q is not at start %q", pane.GetServer(), pane.GetServerStartId())
	}
	if !s.availLive[pane.GetPaneId()] {
		return fmt.Errorf("inventory: pane %q is not on server %q", pane.GetPaneId(), pane.GetServer())
	}
	if avail.GetAvailableForWork() {
		s.avail.set(pane.GetPaneId(), availMark{
			available: true, fresh: avail.GetFreshContextOnReuse(), reason: reason, since: time.Now(),
		})
	} else {
		s.avail.clear(pane.GetPaneId(), reason)
	}
	select {
	case s.dirty <- struct{}{}:
	default:
	}
	return nil
}

// availEvent carries one pane's availability edge, with the pane after the
// change implied by to (the Snapshot carries the level).
func availEvent(server, start, id, session string, from, to availMark, reason typev1.AvailabilityReason) *agentv1.Event {
	ev := &agentv1.AvailabilityChanged{
		Pane:   &typev1.PaneKey{Server: server, ServerStartId: start, PaneId: id},
		From:   from.pb(),
		To:     to.pb(),
		Reason: reason,
	}
	if session != "" {
		ev.SessionId = &typev1.SessionId{Value: session}
	}
	return &agentv1.Event{Body: &agentv1.Event_Availability{Availability: ev}}
}

// pb renders the mark as the contract's Availability: idle always false.
func (m availMark) pb() *typev1.Availability {
	out := &typev1.Availability{}
	if m.available {
		out.AvailableForWork = true
		out.FreshContextOnReuse = m.fresh
		out.SinceTime = timestamppb.New(m.since)
	}
	return out
}
