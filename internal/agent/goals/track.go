package goals

import (
	"context"
	"path/filepath"
	"sort"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/goalfile"
	"github.com/linuxdynasty/fleet-board/internal/agent/repos"
	"github.com/linuxdynasty/fleet-board/internal/agent/ring"
)

// Checkouts maps pane paths to checkouts. *repos.Refresher satisfies it.
type Checkouts interface {
	Workdir(path string) *typev1.PaneWorkdir
	Detail(top string) (repos.Detail, bool)
}

// Episodes persists observed-goal identity. *ring.DB satisfies it, so a
// restart keeps every agent-minted id.
type Episodes interface {
	LiveEpisodes(ctx context.Context) ([]ring.Episode, error)
	SaveEpisode(ctx context.Context, ep ring.Episode) error
	EndEpisode(ctx context.Context, id string) error
}

// Emitter takes GoalState events. It is the observer's Emitter shape, so
// the wiring passes the shared emitter: seq, delivery class and replay
// marking are its job, never the tracker's.
type Emitter interface {
	Emit(ctx context.Context, ev *agentv1.Event) (uint64, error)
}

// Config wires a Tracker. Bound reports registry-bound panes (02h: a pane
// a registry entry names never gains an observed goal); nil binds none,
// which is the whole story until row 21 writes the registry.
type Config struct {
	MachineID string
	Panes     Panes
	Checkouts Checkouts
	Procs     Procs
	Bound     func(key string) bool
	Files     Files
	DB        Episodes
	Emit      Emitter
	Clock     func() time.Time
}

// Tracker attributes eshu goal files to panes (02h "Observed goals").
// Scan runs every rescanInterval: it ends DONE and vanished goals, starts
// newly seen ones and emits a GoalState event per new or changed goal
// (NEVER_DROP). Fill reports the live goals in the Snapshot and binds
// them to their panes and checkouts. Ambiguous lists the folders that
// hold two harness panes, for the status file and doctor. It is not safe
// for concurrent use; one goroutine scans and fills.
type Tracker struct {
	cfg       Config
	primed    bool                          // set after the first scan; events before it are suppressed
	states    map[string]*agentv1.GoalState // live episode id -> last built
	bind      map[string]string             // live episode id -> pane key
	harness   map[string]string             // live episode id -> harness name
	sent      map[string]*agentv1.GoalState // live episode id -> last emitted
	missed    map[string]int                // live episode id -> empty ticks in a row
	paths     map[string]string             // pane key -> last located folder
	ambiguous []string
}

// NewTracker builds a Tracker.
func NewTracker(cfg Config) *Tracker {
	if cfg.Files == nil {
		cfg.Files = OSFiles{}
	}
	if cfg.Clock == nil {
		cfg.Clock = time.Now
	}
	return &Tracker{
		cfg: cfg, states: map[string]*agentv1.GoalState{}, bind: map[string]string{},
		harness: map[string]string{}, sent: map[string]*agentv1.GoalState{}, missed: map[string]int{},
		paths: map[string]string{},
	}
}

// candidate is a pane that may own an observed goal: located, running a
// known harness, and named by no registry entry.
type candidate struct {
	pane     Pane
	path     string
	checkout string
	harness  string
	start    time.Time // earliest harness process start
}

// Scan attributes goal files once. DB failures abort the scan; a pane
// that cannot be located is skipped this tick, never ended on.
func (t *Tracker) Scan(ctx context.Context) error {
	panes, err := t.cfg.Panes.List(ctx)
	if err != nil {
		return err
	}
	live, err := t.cfg.DB.LiveEpisodes(ctx)
	if err != nil {
		return err
	}
	now := t.cfg.Clock()
	cands := t.candidates(ctx, panes)
	byCheckout := map[string][]candidate{}
	for _, c := range cands {
		byCheckout[c.checkout] = append(byCheckout[c.checkout], c)
	}
	ambiguous := map[string]bool{}
	for top, cs := range byCheckout {
		if len(cs) > 1 {
			ambiguous[top] = true
		}
	}
	matched := map[string]bool{} // checkout with a live episode
	for _, ep := range live {
		if t.update(ctx, ep, byCheckout[checkoutOf(ep.Path)]) {
			matched[checkoutOf(ep.Path)] = true
		}
	}
	for top, cs := range byCheckout {
		if len(cs) != 1 || matched[top] {
			continue
		}
		t.attribute(ctx, cs[0], now)
	}
	t.ambiguous = t.ambiguous[:0]
	for top := range ambiguous {
		t.ambiguous = append(t.ambiguous, top)
	}
	sort.Strings(t.ambiguous)
	t.primed = true
	return nil
}

// Primed reports whether a scan ran: the snapshot hook scans once before
// it fills, so the first snapshot already carries the goals.
func (t *Tracker) Primed() bool { return t.primed }

// candidates locates every pane and keeps the attributable ones. Every
// located folder is remembered for Fill, which reports pane workdirs from
// folders no older than a tick.
func (t *Tracker) candidates(ctx context.Context, panes []Pane) []candidate {
	var out []candidate
	for _, p := range panes {
		if p.Dead || t.bound(p) {
			continue
		}
		path, pid, err := t.cfg.Panes.Locate(ctx, p.Server, p.ID)
		if err != nil {
			continue
		}
		if key := keyOf(p); key != "" {
			t.paths[key] = path
		}
		harness, start, ok := t.cfg.Procs.HarnessIn(pid)
		if !ok {
			continue
		}
		wd := t.cfg.Checkouts.Workdir(path)
		if wd == nil || wd.GetCheckoutPath() == "" {
			continue // outside the roots or no checkout: never attributed
		}
		out = append(out, candidate{pane: p, path: path, checkout: wd.GetCheckoutPath(), harness: harness, start: start})
	}
	return out
}

func (t *Tracker) bound(p Pane) bool {
	return t.cfg.Bound != nil && p.Key != nil && t.cfg.Bound(p.Key.String())
}

// update re-reads a live episode's file and maintains it. It reports
// whether the episode stays live. No candidate ends the goal only twice
// in a row: the pane exited (one empty tick may be a tmux hiccup, and a
// hiccup must not mint a new id next tick).
func (t *Tracker) update(ctx context.Context, ep ring.Episode, cs []candidate) bool {
	top := checkoutOf(ep.Path)
	bound := t.bindPane(ep.ID, cs)
	if len(cs) == 0 {
		t.missed[ep.ID]++
		if t.missed[ep.ID] < 2 {
			return true
		}
		t.drop(ctx, ep.ID)
		return false
	}
	delete(t.missed, ep.ID)
	if bound != nil && bound.pane.Dead {
		t.drop(ctx, ep.ID)
		return false
	}
	if last, ok := t.harness[ep.ID]; ok && bound != nil && bound.harness != last {
		t.drop(ctx, ep.ID) // the pane stopped running a harness
		return false
	}
	data, err := t.cfg.Files.Read(ep.Path)
	if err != nil {
		t.drop(ctx, ep.ID) // the goal file went away
		return false
	}
	g := goalfile.Parse(data)
	if g.State == typev1.GoalLineState_GOAL_LINE_STATE_DONE {
		t.finish(ctx, ep, bound, g)
		return false
	}
	st := t.build(ep, bound, g, top)
	t.states[ep.ID] = st
	if bound != nil {
		t.bind[ep.ID], t.harness[ep.ID] = keyOf(bound.pane), bound.harness
	}
	t.maybeEmit(ctx, ep.ID, st)
	return true
}

// attribute starts an episode for a checkout's single candidate pane.
func (t *Tracker) attribute(ctx context.Context, c candidate, now time.Time) {
	path, _, ok, err := t.cfg.Files.Select(c.checkout, c.start)
	if err != nil || !ok {
		return // stale or absent: a leftover file is never attributed
	}
	data, err := t.cfg.Files.Read(path)
	if err != nil {
		return
	}
	g := goalfile.Parse(data)
	if g.State == typev1.GoalLineState_GOAL_LINE_STATE_DONE {
		return // DONE at first sight shows nothing
	}
	ep := ring.Episode{ID: MintID(t.cfg.MachineID, path, now), Path: path, FirstSeen: now.UnixNano()}
	if err := t.cfg.DB.SaveEpisode(ctx, ep); err != nil {
		return
	}
	st := t.build(ep, &c, g, c.checkout)
	t.states[ep.ID] = st
	t.bind[ep.ID], t.harness[ep.ID] = keyOf(c.pane), c.harness
	t.maybeEmit(ctx, ep.ID, st)
}

// bindPane settles which candidate pane an episode reports: the single
// candidate when there is one, the remembered one while it still owns the
// checkout, else none (ambiguous, never guessed).
func (t *Tracker) bindPane(id string, cs []candidate) *candidate {
	if len(cs) == 1 {
		return &cs[0]
	}
	if last, ok := t.bind[id]; ok {
		for i := range cs {
			if keyOf(cs[i].pane) == last {
				return &cs[i]
			}
		}
	}
	return nil
}

// finish ends a DONE episode: one terminal event, then silence. The event
// is never suppressed: the snapshot will not list the goal, so no snapshot
// could carry it.
func (t *Tracker) finish(ctx context.Context, ep ring.Episode, bound *candidate, g goalfile.File) {
	t.emit(ctx, t.build(ep, bound, g, checkoutOf(ep.Path)))
	t.drop(ctx, ep.ID)
}

// drop ends an episode without an event: the next snapshot no longer
// lists it, which is how the hub learns OBSERVED_GONE (03k).
func (t *Tracker) drop(ctx context.Context, id string) {
	_ = t.cfg.DB.EndEpisode(ctx, id)
	delete(t.states, id)
	delete(t.bind, id)
	delete(t.harness, id)
	delete(t.sent, id)
	delete(t.missed, id)
}

// maybeEmit sends the GoalState when it changed. The tracker's first scan
// sends nothing: the connect snapshot carries it (as the observer's first
// read sends no events either).
func (t *Tracker) maybeEmit(ctx context.Context, id string, st *agentv1.GoalState) {
	if proto.Equal(t.sent[id], st) {
		return
	}
	t.sent[id] = st
	if t.primed {
		t.emit(ctx, st)
	}
}

func (t *Tracker) emit(ctx context.Context, st *agentv1.GoalState) {
	if t.cfg.Emit == nil {
		return
	}
	// An emit failure is the emitter's to count as loss (the hub asks for
	// a snapshot); it never ends the goal.
	_, _ = t.cfg.Emit.Emit(ctx, &agentv1.Event{Body: &agentv1.Event_GoalState{GoalState: st}})
}

// build renders one GoalState. observed_time is the first sight, not the
// read time, so the hub's start_time stays put (03k).
func (t *Tracker) build(ep ring.Episode, bound *candidate, g goalfile.File, top string) *agentv1.GoalState {
	st := &agentv1.GoalState{
		GoalId: &typev1.GoalId{Value: ep.ID}, FirstLine: g.FirstLine, ConsentLine: g.ConsentLine,
		State: g.State, WatchPid: g.WatchPID, Owner: g.Owner, Action: g.Action,
		WorktreePath: top, ObservedTime: timestamppb.New(time.Unix(0, ep.FirstSeen).UTC()),
		Binding: agentv1.GoalBindingSource_GOAL_BINDING_SOURCE_OBSERVED,
	}
	if rel, err := filepath.Rel(top, ep.Path); err == nil {
		st.GoalFile = rel
	} else {
		st.GoalFile = ep.Path
	}
	if bound != nil {
		st.Pane, st.SessionId = bound.pane.Key, bound.pane.Session
	}
	if d, ok := t.cfg.Checkouts.Detail(top); ok {
		st.HeadCommit, st.Dirty, st.Branch = d.Head, d.Dirty, d.Branch
	}
	if g.State == typev1.GoalLineState_GOAL_LINE_STATE_BLOCKED_WATCH {
		st.WatchAlive = t.cfg.Procs.Alive(g.WatchPID)
	}
	return st
}

// Fill reports the live goals in the snapshot, the pane workdirs from
// the last tick's locations, and binds the goals to their panes and
// checkouts (TmuxPane.goal, BranchState.goal_id).
func (t *Tracker) Fill(snap *agentv1.Snapshot) {
	for _, srv := range snap.GetTmuxServers() {
		for _, sess := range srv.GetSessions() {
			for _, w := range sess.GetWindows() {
				for _, p := range w.GetPanes() {
					if path, ok := t.paths[p.GetKey().String()]; ok {
						p.Workdir = t.cfg.Checkouts.Workdir(path)
					}
				}
			}
		}
	}
	live := make([]*agentv1.GoalState, 0, len(t.states))
	ids := make([]string, 0, len(t.states))
	for id := range t.states {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		live = append(live, t.states[id])
	}
	snap.Goals = live
	for _, st := range live {
		id := st.GetGoalId().GetValue()
		if key := t.bind[id]; key != "" {
			for _, srv := range snap.GetTmuxServers() {
				for _, sess := range srv.GetSessions() {
					for _, w := range sess.GetWindows() {
						for _, p := range w.GetPanes() {
							if p.GetKey().String() == key {
								p.Goal = &typev1.GoalId{Value: id}
							}
						}
					}
				}
			}
		}
		for _, rc := range snap.GetRepos() {
			if rc.GetPath() == st.GetWorktreePath() && rc.GetBranch() != nil {
				rc.Branch.GoalId = &typev1.GoalId{Value: id}
			}
		}
	}
}

// Ambiguous lists the folders holding two harness panes, for the status
// file and doctor (02h "Observed goals").
func (t *Tracker) Ambiguous() []string { return t.ambiguous }

// checkoutOf is the checkout holding a goal file: its grandparent
// (<top>/.claude/active-goal.<session>).
func checkoutOf(path string) string { return filepath.Dir(filepath.Dir(path)) }

func keyOf(p Pane) string {
	if p.Key == nil {
		return ""
	}
	return p.Key.String()
}
