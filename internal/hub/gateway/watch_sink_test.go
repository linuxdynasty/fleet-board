package gateway_test

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/gateway"
	"github.com/linuxdynasty/fleet-board/internal/hub/ingest"
	"github.com/linuxdynasty/fleet-board/internal/hub/observed"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// watchRecorder records the WatchSink calls on the handler goroutines.
type watchRecorder struct {
	mu        sync.Mutex
	states    []stateCall
	created   []createdCall
	committed []string
	events    []string
	goals     []goalsCall
	panes     []gateway.PanePublication
}

type stateCall struct{ id, from string }

type createdCall struct {
	id     string
	replay bool
}

type goalsCall struct {
	id     string
	before int
	// after is how many stamped after-images the job captured in its
	// transaction: the publish carries each job's own writes.
	after int
}

func (r *watchRecorder) sink() gateway.WatchSink {
	return gateway.WatchSink{
		MachineState: func(_ context.Context, id, from string) {
			r.mu.Lock()
			r.states = append(r.states, stateCall{id, from})
			r.mu.Unlock()
		},
		MachineCreated: func(_ context.Context, id string, replay bool) {
			r.mu.Lock()
			r.created = append(r.created, createdCall{id, replay})
			r.mu.Unlock()
		},
		MachineCommitted: func(_ context.Context, id string, _ *statedb.MachineRow) {
			r.mu.Lock()
			r.committed = append(r.committed, id)
			r.mu.Unlock()
		},
		MachineEvent: func(_ context.Context, id string) {
			r.mu.Lock()
			r.events = append(r.events, id)
			r.mu.Unlock()
		},
		GoalsCommitted: func(_ context.Context, id string, before, after []statedb.GoalRow) {
			r.mu.Lock()
			r.goals = append(r.goals, goalsCall{id, len(before), len(after)})
			r.mu.Unlock()
		},
		Pane: func(_ context.Context, p gateway.PanePublication) {
			r.mu.Lock()
			r.panes = append(r.panes, p)
			r.mu.Unlock()
		},
	}
}

// waitSink polls until cond holds over a snapshot of the calls.
func (r *watchRecorder) waitSink(t *testing.T, what string, cond func(*watchRecorder) bool) {
	t.Helper()
	end := time.Now().Add(10 * time.Second)
	for {
		r.mu.Lock()
		ok := cond(r)
		r.mu.Unlock()
		if ok {
			return
		}
		if time.Now().After(end) {
			t.Fatalf("timed out waiting for Watch sink %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// goalEvent is a NEVER_DROP goal_state event of seq carrying line.
func goalEvent(seq uint64, line string) *agentv1.ConnectRequest {
	return &agentv1.ConnectRequest{Body: &agentv1.ConnectRequest_Event{Event: &agentv1.Event{
		Seq: seq, Delivery: agentv1.DeliveryClass_DELIVERY_CLASS_NEVER_DROP,
		Body: &agentv1.Event_GoalState{GoalState: &agentv1.GoalState{
			GoalId:       &typev1.GoalId{Value: "obs-1"},
			Pane:         &typev1.PaneKey{Server: "default", ServerStartId: "s1", PaneId: "%1"},
			SessionId:    &typev1.SessionId{Value: "sess-1"},
			FirstLine:    line,
			State:        typev1.GoalLineState_GOAL_LINE_STATE_WORKING,
			HeadCommit:   "abc123",
			GoalFile:     ".claude/active-goal.s1",
			WorktreePath: "/repos/a",
			Branch:       "fleet/12",
			ObservedTime: timestamppb.New(time.Unix(1000, 0).UTC()),
			Binding:      agentv1.GoalBindingSource_GOAL_BINDING_SOURCE_OBSERVED,
		}},
	}}}
}

// paneEvent is a SHED pane_summary event of seq for the pane.
func paneEvent(seq uint64, paneID, lines string, at time.Time) *agentv1.ConnectRequest {
	return &agentv1.ConnectRequest{Body: &agentv1.ConnectRequest_Event{Event: &agentv1.Event{
		Seq: seq, Delivery: agentv1.DeliveryClass_DELIVERY_CLASS_SHED,
		Body: &agentv1.Event_PaneSummary{PaneSummary: &agentv1.PaneSummary{
			Pane:           &typev1.PaneKey{Server: "default", ServerStartId: "s1", PaneId: paneID},
			LastLines:      lines,
			LastOutputTime: timestamppb.New(at),
			Activity:       typev1.Activity_ACTIVITY_ACTIVE,
		}},
	}}}
}

// snapshotWithGoal is snapshotMsg plus one observed goal and a pane the
// tmux inventory attributes to session sess-9.
func snapshotWithGoal(seq uint64, line string) *agentv1.ConnectRequest {
	msg := snapshotMsg(seq, "laptop", "h1")
	snap := msg.GetSnapshot()
	snap.Goals = []*agentv1.GoalState{goalEvent(0, line).GetEvent().GetGoalState()}
	snap.TmuxServers = []*agentv1.TmuxServer{{
		Kind: typev1.TmuxServerKind_TMUX_SERVER_KIND_FLEET, SocketName: "default",
		ServerStartId: "s1", Running: true, Observed: true,
		Sessions: []*agentv1.TmuxSession{{
			TmuxId: "0", Name: "work", SessionId: &typev1.SessionId{Value: "sess-9"},
			Windows: []*agentv1.TmuxWindow{{
				Key: &typev1.WindowKey{Server: "default", ServerStartId: "s1", WindowId: "@0"},
				Panes: []*agentv1.TmuxPane{{
					Key: &typev1.PaneKey{Server: "default", ServerStartId: "s1", PaneId: "%1"},
				}},
			}},
		}},
	}}
	return msg
}

func startWatchedHub(t *testing.T, r *watchRecorder) *hub {
	t.Helper()
	return startHub(t, func(o *gateway.ServerOptions) {
		o.Watch = r.sink()
		o.Appliers = []ingest.Applier{observed.Applier{}}
		o.EventAppliers = []ingest.EventApplier{observed.Applier{}}
	})
}

// A Snapshot commit publishes the machine write, its goal writes and the
// ONLINE state change after the commit, with the pre-submit before image
// (empty on a first snapshot); the enroll published the creation first.
func TestWatchSinkSnapshotPublishes(t *testing.T) {
	t.Parallel()
	var rec watchRecorder
	h := startWatchedHub(t, &rec)
	m := h.enrollMachine(t)
	rec.waitSink(t, "enroll creation", func(r *watchRecorder) bool {
		for _, c := range r.created {
			if c.id == m.id && !c.replay {
				return true
			}
		}
		return false
	})
	s := h.open(t, m)
	s.send(t, hello(m, 0))
	s.ack(t)
	s.send(t, snapshotWithGoal(5, "Working"))
	rec.waitSink(t, "online state", func(r *watchRecorder) bool {
		for _, st := range r.states {
			if st.id == m.id && st.from == "ENROLLED" {
				return true
			}
		}
		return false
	})
	rec.waitSink(t, "snapshot machine", func(r *watchRecorder) bool {
		for _, id := range r.committed {
			if id == m.id {
				return true
			}
		}
		return false
	})
	rec.waitSink(t, "snapshot goals", func(r *watchRecorder) bool {
		for _, g := range r.goals {
			if g.id == m.id && g.before == 0 && g.after == 1 {
				return true
			}
		}
		return false
	})
}

// An applied goal_state event publishes its machine stamp and the goal
// write with the pre-submit before image; a SHED pane_summary event
// publishes the pane attributed to the snapshot's session.
func TestWatchSinkEventsPublish(t *testing.T) {
	t.Parallel()
	var rec watchRecorder
	h := startWatchedHub(t, &rec)
	m := h.enrollMachine(t)
	s := h.open(t, m)
	s.send(t, hello(m, 0))
	s.ack(t)
	s.send(t, snapshotWithGoal(5, "Working"))
	h.waitRow(t, m.id, "ONLINE", func(r machineRow) bool { return r.state == "ONLINE" })
	rec.waitSink(t, "snapshot goals", func(r *watchRecorder) bool { return len(r.goals) > 0 })
	s.send(t, goalEvent(6, "Working harder"))
	rec.waitSink(t, "event machine", func(r *watchRecorder) bool {
		for _, id := range r.events {
			if id == m.id {
				return true
			}
		}
		return false
	})
	rec.waitSink(t, "event goals with before", func(r *watchRecorder) bool {
		for _, g := range r.goals {
			if g.id == m.id && g.before == 1 && g.after == 1 {
				return true
			}
		}
		return false
	})
	s.send(t, paneEvent(7, "%1", "output", time.Unix(2000, 0).UTC()))
	rec.waitSink(t, "attributed pane", func(r *watchRecorder) bool {
		for _, p := range r.panes {
			if p.MachineID == m.id && p.SessionID == "sess-9" && p.LastLines == "output" &&
				p.Pane.GetPaneId() == "%1" && p.Seq > 0 {
				return true
			}
		}
		return false
	})
}

// Closing the stream publishes the OFFLINE state change after the close
// commits.
func TestWatchSinkClosePublishesOffline(t *testing.T) {
	t.Parallel()
	var rec watchRecorder
	h := startWatchedHub(t, &rec)
	m := h.enrollMachine(t)
	s := h.open(t, m)
	s.send(t, hello(m, 0))
	s.ack(t)
	s.send(t, snapshotWithGoal(5, "Working"))
	rec.waitSink(t, "online", func(r *watchRecorder) bool { return len(r.states) > 0 })
	s.send(t, agentBye())
	if err := s.ended(t); err != nil {
		t.Fatal(err)
	}
	rec.waitSink(t, "offline state", func(r *watchRecorder) bool {
		for _, st := range r.states {
			if st.id == m.id && st.from == "ONLINE" {
				return true
			}
		}
		return false
	})
}

// blockApplier holds a Snapshot writer job inside its transaction until
// unblock closes, so a test can end the stream mid-wait: the arm reads its
// before images and submits while live, abandons the wait when the agent
// leaves, and the teardown publishes a job that commits afterwards.
type blockApplier struct {
	started chan struct{}
	unblock chan struct{}
}

func (b *blockApplier) Apply(_ context.Context, _ *sql.Tx, _ string, _ *agentv1.Snapshot) ([]statedb.GoalRow, error) {
	close(b.started)
	select {
	case <-b.unblock:
		return nil, nil
	case <-time.After(20 * time.Second):
		return nil, errors.New("gateway_test: blockApplier stuck")
	}
}

// A Snapshot job that commits after its wait was abandoned still publishes
// its committed goal writes with its machine write at the teardown: the
// abandoned entry carries the pre-submit before images, so GOALS watchers
// see the change and resume replay has the goal entry.
func TestWatchSinkAbandonedSnapshotPublishesGoals(t *testing.T) {
	t.Parallel()
	var rec watchRecorder
	block := &blockApplier{started: make(chan struct{}), unblock: make(chan struct{})}
	h := startHub(t, func(o *gateway.ServerOptions) {
		o.Watch = rec.sink()
		o.Appliers = []ingest.Applier{block, observed.Applier{}}
		o.EventAppliers = []ingest.EventApplier{observed.Applier{}}
	})
	m := h.enrollMachine(t)
	s := h.open(t, m)
	s.send(t, hello(m, 0))
	s.ack(t)
	s.send(t, snapshotWithGoal(5, "Working"))
	select {
	case <-block.started:
	case <-time.After(10 * time.Second):
		t.Fatal("the Snapshot job never started")
	}
	leave(s)
	h.waitEvents(t, "snapshot.abandoned", 1)
	close(block.unblock)
	h.waitClosed(t, 1)
	rec.waitSink(t, "abandoned goals", func(r *watchRecorder) bool {
		for _, g := range r.goals {
			if g.id == m.id && g.before == 0 && g.after == 1 {
				return true
			}
		}
		return false
	})
	rec.waitSink(t, "abandoned machine", func(r *watchRecorder) bool {
		for _, id := range r.committed {
			if id == m.id {
				return true
			}
		}
		return false
	})
}

// A Snapshot commit publishes the goal writes before the machine write:
// the goal appliers stamp before MarkOnline stamps the machine, so goals
// first is ascending stamp order and a resume from a goal never skips it.
func TestWatchSinkSnapshotPublishesGoalsBeforeMachine(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var order []string
	done := make(chan struct{})
	var once sync.Once
	sink := (&watchRecorder{}).sink()
	sink.GoalsCommitted = func(_ context.Context, id string, _, _ []statedb.GoalRow) {
		mu.Lock()
		order = append(order, "goals")
		if len(order) == 2 {
			once.Do(func() { close(done) })
		}
		mu.Unlock()
	}
	sink.MachineCommitted = func(_ context.Context, _ string, _ *statedb.MachineRow) {
		mu.Lock()
		order = append(order, "machine")
		if len(order) == 2 {
			once.Do(func() { close(done) })
		}
		mu.Unlock()
	}
	h := startHub(t, func(o *gateway.ServerOptions) {
		o.Watch = sink
		o.Appliers = []ingest.Applier{observed.Applier{}}
		o.EventAppliers = []ingest.EventApplier{observed.Applier{}}
	})
	m := h.enrollMachine(t)
	s := h.open(t, m)
	s.send(t, hello(m, 0))
	s.ack(t)
	s.send(t, snapshotWithGoal(5, "Working"))
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for snapshot publishes")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(order) != 2 || order[0] != "goals" || order[1] != "machine" {
		t.Fatalf("publish order = %v, want [goals machine]", order)
	}
}
