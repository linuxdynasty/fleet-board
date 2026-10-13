package inventory

import (
	"google.golang.org/protobuf/proto"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// diff returns the events that take the hub from old to next: two reads of
// the same server start. Each event carries the object after the change
// (02i: the hub applies it as an upsert); a CLOSED one carries ids only.
// Order: what is created (sessions, then windows, then panes), what changed,
// then what went (panes, then windows, then sessions), so a hub applying
// them in seq order never sees a child without its parent.
//
// A pane first seen dead is created and exited (E3), so the one-nudge
// rule still runs. A created session or window carries its windows and
// panes; a renamed or updated one carries its own fields only. Panes are
// keyed by pane id across the server: a grouped pair lists a pane under
// both sessions, and its PaneCreated and PaneExited are sent once. A pane's or window's focus
// (active) raises nothing on its own (02h lists what PaneChanged is for);
// the Snapshot carries it as a level.
func diff(old, next *agentv1.TmuxServer) []*agentv1.Event {
	o, n := index(old), index(next)
	var created, changed, gone []*agentv1.Event
	for _, s := range next.GetSessions() {
		was, ok := o.sessions[s.GetTmuxId()]
		switch {
		case !ok:
			created = append(created, sessionEvent(next, agentv1.ChangeKind_CHANGE_KIND_CREATED, s))
		case was.GetName() != s.GetName():
			changed = append(changed, sessionEvent(next, agentv1.ChangeKind_CHANGE_KIND_RENAMED, own(s)))
		case !proto.Equal(own(was), own(s)):
			changed = append(changed, sessionEvent(next, agentv1.ChangeKind_CHANGE_KIND_UPDATED, own(s)))
		}
		if !ok {
			continue // its windows ride in its CREATED event
		}
		for _, w := range s.GetWindows() {
			k := windowKey{s.GetTmuxId(), w.GetKey().GetWindowId()}
			wasW, ok := o.windows[k]
			switch {
			case !ok:
				created = append(created, windowEvent(agentv1.ChangeKind_CHANGE_KIND_CREATED, s, w))
			case wasW.GetName() != w.GetName():
				changed = append(changed, windowEvent(agentv1.ChangeKind_CHANGE_KIND_RENAMED, s, ownW(w)))
			case !proto.Equal(ownW(wasW), ownW(w)):
				changed = append(changed, windowEvent(agentv1.ChangeKind_CHANGE_KIND_UPDATED, s, ownW(w)))
			}
		}
	}
	for _, id := range n.paneOrder {
		p := n.panes[id]
		was, ok := o.panes[id]
		switch {
		case !ok:
			created = append(created, &agentv1.Event{Body: &agentv1.Event_PaneCreated{PaneCreated: &agentv1.PaneCreated{Pane: p}}})
			if p.GetDead() { // lived and died between two reads: still an exit (E3)
				created = append(created, exited(p, p.ExitStatus, true))
			}
			continue
		case !was.GetDead() && p.GetDead():
			changed = append(changed, exited(p, p.ExitStatus, true))
		case statusLanded(was, p):
			// The exit status landed after the death was read: the hub
			// already has the status-less PaneExited, so the status rides
			// one PaneChanged (the paneFacts compare below would hide a
			// status-only change, and anything else changed rides along:
			// the event carries the whole pane).
			changed = append(changed, &agentv1.Event{Body: &agentv1.Event_PaneChanged{PaneChanged: &agentv1.PaneChanged{Pane: p}}})
			continue
		}
		if !proto.Equal(paneFacts(was, p), paneFacts(p, p)) {
			changed = append(changed, &agentv1.Event{Body: &agentv1.Event_PaneChanged{PaneChanged: &agentv1.PaneChanged{Pane: p}}})
		}
	}
	for _, id := range o.paneOrder {
		if _, ok := n.panes[id]; !ok {
			p := o.panes[id]
			gone = append(gone, exited(p, p.ExitStatus, false))
		}
	}
	for _, s := range old.GetSessions() {
		for _, w := range s.GetWindows() {
			if _, ok := n.windows[windowKey{s.GetTmuxId(), w.GetKey().GetWindowId()}]; !ok {
				gone = append(gone, windowEvent(agentv1.ChangeKind_CHANGE_KIND_CLOSED, s, &agentv1.TmuxWindow{Key: w.GetKey()}))
			}
		}
	}
	for _, s := range old.GetSessions() {
		if _, ok := n.sessions[s.GetTmuxId()]; !ok {
			gone = append(gone, sessionEvent(old, agentv1.ChangeKind_CHANGE_KIND_CLOSED, &agentv1.TmuxSession{TmuxId: s.GetTmuxId()}))
		}
	}
	return append(append(created, changed...), gone...)
}

type windowKey struct{ session, window string }

type serverIndex struct {
	sessions  map[string]*agentv1.TmuxSession
	windows   map[windowKey]*agentv1.TmuxWindow
	panes     map[string]*agentv1.TmuxPane
	paneOrder []string // first appearance, in session order
}

func index(srv *agentv1.TmuxServer) serverIndex {
	x := serverIndex{
		sessions: map[string]*agentv1.TmuxSession{},
		windows:  map[windowKey]*agentv1.TmuxWindow{},
		panes:    map[string]*agentv1.TmuxPane{},
	}
	for _, s := range srv.GetSessions() {
		x.sessions[s.GetTmuxId()] = s
		for _, w := range s.GetWindows() {
			x.windows[windowKey{s.GetTmuxId(), w.GetKey().GetWindowId()}] = w
			for _, p := range w.GetPanes() {
				id := p.GetKey().GetPaneId()
				if _, ok := x.panes[id]; !ok {
					x.panes[id] = p
					x.paneOrder = append(x.paneOrder, id)
				}
			}
		}
	}
	return x
}

// own is a session without its windows; ownW a window without its panes
// and its focus (switching windows raises nothing on its own).
func own(s *agentv1.TmuxSession) *agentv1.TmuxSession {
	c := proto.CloneOf(s)
	c.Windows = nil
	return c
}

func ownW(w *agentv1.TmuxWindow) *agentv1.TmuxWindow {
	c := proto.CloneOf(w)
	c.Panes, c.Active = nil, false
	return c
}

// statusLanded reports that an already-dead pane's exit status arrived
// after the death was read: dead before with none, dead now with one.
func statusLanded(was, p *agentv1.TmuxPane) bool {
	return was.GetDead() && p.GetDead() && was.ExitStatus == nil && p.ExitStatus != nil
}

// paneFacts is p with what never raises PaneChanged taken out (focus), and
// the death fields taken from death, so a death alone is PaneExited's.
func paneFacts(p, death *agentv1.TmuxPane) *agentv1.TmuxPane {
	c := proto.CloneOf(p)
	c.Active = false
	if death.GetDead() {
		c.Dead, c.ExitStatus = death.GetDead(), death.ExitStatus
	}
	return c
}

func sessionEvent(srv *agentv1.TmuxServer, kind agentv1.ChangeKind, s *agentv1.TmuxSession) *agentv1.Event {
	return &agentv1.Event{Body: &agentv1.Event_Session{Session: &agentv1.SessionEvent{
		Kind: kind, ServerKind: srv.GetKind(), ServerStartId: srv.GetServerStartId(), Session: s,
	}}}
}

func windowEvent(kind agentv1.ChangeKind, s *agentv1.TmuxSession, w *agentv1.TmuxWindow) *agentv1.Event {
	return &agentv1.Event{Body: &agentv1.Event_Window{Window: &agentv1.WindowEvent{Kind: kind, SessionTmuxId: s.GetTmuxId(), Window: w}}}
}

func exited(p *agentv1.TmuxPane, status *int32, remains bool) *agentv1.Event {
	key := proto.CloneOf(p.GetKey())
	if key == nil {
		key = &typev1.PaneKey{}
	}
	return &agentv1.Event{Body: &agentv1.Event_PaneExited{PaneExited: &agentv1.PaneExited{Pane: key, ExitStatus: status, Remains: remains}}}
}
