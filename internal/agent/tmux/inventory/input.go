package inventory

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux"
)

// inputSub is the one session-activity subscription per server (02h E13):
// tmux checks it about once a second and sends %subscription-changed when
// any session's activity time moves. Any change only dirties the client
// read below; the read carries who typed, at which pane. Its value is
// never parsed: session ids would name where, but the read says it with
// the client that moved.
var inputSub = tmux.Subscription{
	Name:   "fleet-input",
	Format: "#{S:#{session_id}=#{session_activity} }",
}

// defaultInputGap is the least time between two PaneInput events of one
// pane (events.proto: at most one per pane per 10 s, latest wins). The
// level keeps every key; only the event is gated.
const defaultInputGap = 10 * time.Second

// inputPollGap is the backup list-clients poll while the input
// subscription is unconfirmed (ADR 0024 decision 9): from attach until the
// first %subscription-changed confirms it, and after %exit until it is
// confirmed again. No poll runs while the subscription is live, so an idle
// server costs no command (7f proof 6).
const inputPollGap = 2 * time.Second

// inputCredit is one pane a client read attributed a key to.
type inputCredit struct {
	pane string // "%N"
	at   time.Time
}

// inputTracker keeps E13 state for one server. The loop owns it (see the
// inMu note on server): it feeds one client read through evaluate and
// emits what evaluate returns. Only a person counts: the agent's own
// observer is a control-mode client and is never stored. A client seen
// first counts as input (02h: when unsure, it counts, and a person who
// attached is at the keyboard); then only a moved key time counts, at the
// client's active pane, so the agent's own send-keys, new-session,
// resizes and captures credit nothing. A server restart reuses no state
// (reset): pane ids restart with a new server, and a person still attached
// counts as input again.
type inputTracker struct {
	now   func() time.Time
	gap   time.Duration
	last  map[int]time.Time    // client pid to its key time
	emit  map[string]time.Time // pane "%N" to its latest PaneInput
	input map[string]time.Time // pane "%N" to its latest key (the level)
}

func newInputTracker(now func() time.Time, gap time.Duration) *inputTracker {
	return &inputTracker{
		now: now, gap: gap, last: map[int]time.Time{},
		emit: map[string]time.Time{}, input: map[string]time.Time{},
	}
}

// evaluate folds one read's clients into the state and returns one credit
// per pane that saw a key, in pane order. known tells whether a pane is in
// the last inventory: the loop passes that (a session gone between the
// change and the read credits nothing, and emits nothing for a ghost);
// CheckInput passes allow-all (a gate, not an event). The level keeps
// every allowed key, even inside the event gate.
func (t *inputTracker) evaluate(clients []tmux.ClientInfo, known func(string) bool) []inputCredit {
	now := t.now()
	rest := append([]tmux.ClientInfo(nil), clients...)
	sort.Slice(rest, func(i, j int) bool {
		if rest[i].Pane != rest[j].Pane {
			return rest[i].Pane < rest[j].Pane
		}
		return rest[i].PID < rest[j].PID
	})
	byPane := map[string]time.Time{}
	for _, cl := range rest {
		if cl.Control {
			continue
		}
		pane := cl.Pane.String()
		if !known(pane) {
			// No baseline: a pane the inventory has not read yet
			// credits on a later read, once known (a ghost never
			// does). Baselining it here would lose the key that
			// announced the session while the inventory catches up.
			continue
		}
		prev, ok := t.last[cl.PID]
		t.last[cl.PID] = cl.Activity
		if ok && !cl.Activity.After(prev) {
			continue
		}
		if at, dup := byPane[pane]; !dup || cl.Activity.After(at) {
			byPane[pane] = cl.Activity
		}
	}
	for pane, at := range byPane {
		if prev, dup := t.input[pane]; !dup || at.After(prev) {
			t.input[pane] = at
		}
	}
	var credits []inputCredit
	for _, pane := range sortedPanes(byPane) {
		if prev, ok := t.emit[pane]; ok && now.Sub(prev) < t.gap {
			continue
		}
		t.emit[pane] = now
		credits = append(credits, inputCredit{pane: pane, at: byPane[pane]})
	}
	keep := make(map[int]bool, len(rest))
	for _, cl := range rest {
		if !cl.Control {
			keep[cl.PID] = true
		}
	}
	for pid := range t.last {
		if !keep[pid] {
			delete(t.last, pid)
		}
	}
	return credits
}

func sortedPanes(byPane map[string]time.Time) []string {
	out := make([]string, 0, len(byPane))
	for pane := range byPane {
		out = append(out, pane)
	}
	sort.Strings(out)
	return out
}

// stamp sets each pane's last-input time from the tracked state: the
// Snapshot and every event carrying a pane show the level (01 E13). A pane
// with no key keeps its zero value (unset = never seen).
func (t *inputTracker) stamp(srv *agentv1.TmuxServer) {
	if srv == nil {
		return
	}
	for _, s := range srv.GetSessions() {
		for _, w := range s.GetWindows() {
			for _, p := range w.GetPanes() {
				if at, ok := t.input[p.GetKey().GetPaneId()]; ok {
					p.LastInputTime = timestamppb.New(at)
				}
			}
		}
	}
}

// reset forgets every client and pane: a server restart reuses no state.
func (t *inputTracker) reset() {
	t.last = map[int]time.Time{}
	t.emit = map[string]time.Time{}
	t.input = map[string]time.Time{}
}

// readInput reads the server's clients now. Every ended read is counted
// for the input_reads counter, by result; a read the client refused as
// not attached, or cut short by the agent's stop, is not counted (the
// client's state says why). A failed read keeps the last client times.
func (s *server) readInput(ctx context.Context) ([]tmux.ClientInfo, error) {
	rctx, cancel := context.WithTimeout(ctx, readTimeout)
	clients, err := s.c.ReadClients(rctx)
	cancel()
	if err != nil {
		if ctx.Err() == nil && !errors.Is(err, tmux.ErrNotAttached) {
			s.o.tel.inputRead(ctx, s.kind, readErrKind(err))
			s.o.tel.inputFailed(ctx, s.kind, err)
		}
		return nil, err
	}
	s.o.tel.inputRead(ctx, s.kind, "ok")
	return clients, nil
}

// syncInput reads the server's clients now and emits one PaneInput per
// pane that saw a key (02h E13). A failed read is counted and the last
// client times stand until the next one. Panes the inventory never held
// credit nothing, and a server the client already left (a restart the
// sync has not reset for yet) waits for that reset: its pane ids are
// another server's.
func (s *server) syncInput(ctx context.Context) {
	if s.c.State() != tmux.StateUp {
		return
	}
	if s.last != nil && s.last.GetServerStartId() != s.c.ServerStartID() {
		return
	}
	clients, err := s.readInput(ctx)
	if err != nil {
		return
	}
	s.inMu.Lock()
	credits := s.inputs.evaluate(clients, s.knownPane)
	s.inputs.stamp(s.last)
	start := ""
	if s.last != nil {
		start = s.last.GetServerStartId()
	}
	s.inMu.Unlock()
	for _, cr := range credits {
		key := &typev1.PaneKey{Server: s.name, ServerStartId: start, PaneId: cr.pane}
		s.emit(ctx, &agentv1.Event{Body: &agentv1.Event_PaneInput{PaneInput: &agentv1.PaneInput{
			Pane: key, InputTime: timestamppb.New(cr.at),
		}}})
		s.o.tel.paneInput(ctx, s.kind)
	}
}

// knownPane tells whether a pane id is in the last inventory.
func (s *server) knownPane(pane string) bool {
	if s.last == nil {
		return false
	}
	for _, sess := range s.last.GetSessions() {
		for _, w := range sess.GetWindows() {
			for _, p := range w.GetPanes() {
				if p.GetKey().GetPaneId() == pane {
					return true
				}
			}
		}
	}
	return false
}

// CheckInput reads the server's clients now and reports when pane last
// saw a key (row 18's SendKeys check calls this at that moment, never
// stored push state, 02h E13). It stamps the level but emits nothing, so a
// missed push cannot open the gate. An unknown server, or a client that is
// down, is an error; a pane with no key is the zero time with no error.
func (o *Observer) CheckInput(ctx context.Context, server, pane string) (time.Time, error) {
	s := o.servers[server]
	if s == nil {
		return time.Time{}, fmt.Errorf("inventory: input: unknown server %q", server)
	}
	if s.c.State() != tmux.StateUp {
		return time.Time{}, tmux.ErrNotAttached
	}
	clients, err := s.readInput(ctx)
	if err != nil {
		return time.Time{}, fmt.Errorf("inventory: input: the client read failed: %w", err)
	}
	allow := func(string) bool { return true }
	s.inMu.Lock()
	s.inputs.evaluate(clients, allow)
	s.inputs.stamp(s.last)
	at := s.inputs.input[pane]
	s.inMu.Unlock()
	return at, nil
}
