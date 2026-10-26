package goalregistry

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/goalfile"
)

var (
	// ErrPane means the pane key is missing or has no pane id.
	ErrPane = errors.New("goalregistry: the pane key is missing")
	// ErrGoalID means the goal id is not a safe id.
	ErrGoalID = errors.New("goalregistry: the goal id is not a safe id")
)

// Emit is one event into the agent's ring (NEVER_DROP for the registry
// and hook kinds). Production passes the events emitter's Emit.
type Emit func(ctx context.Context, ev *agentv1.Event) (uint64, error)

// Registry is the agent's goal registry: one entry per pane. The zero
// value is unusable; use New. Every method is safe for concurrent use;
// hook calls (match.go, hook.go) read while commands write.
type Registry struct {
	mu     sync.Mutex
	byPane map[string]*agentv1.GoalRegistration
	emit   Emit
	now    func() time.Time
}

// New builds a Registry. emit may be nil (no change events); now may be
// nil (the wall clock).
func New(emit Emit, now func() time.Time) *Registry {
	if now == nil {
		now = time.Now
	}
	return &Registry{byPane: map[string]*agentv1.GoalRegistration{}, emit: emit, now: now}
}

// paneString keys entries the way tmux identifies a pane: server,
// server start and pane id.
func paneString(k *typev1.PaneKey) string {
	if k == nil {
		return ""
	}
	return k.GetServer() + "\x00" + k.GetServerStartId() + "\x00" + k.GetPaneId()
}

// checkID applies the ids.proto id rule the registry relies on (the
// goalfile package owns the rule; the file name depends on it too).
func checkID(goal string) error {
	if !goalfile.ValidGoalID(goal) {
		return fmt.Errorf("%w: %q", ErrGoalID, goal)
	}
	return nil
}

// Register binds goal to pane for harness in worktreePath, source
// REGISTRY. A second goal in the same pane replaces the first and
// reports replaced (02h). The change emits GoalRegistryChanged
// (CREATED, or UPDATED for a replace).
func (r *Registry) Register(pane *typev1.PaneKey, goal, harness, worktreePath string, repoDriver bool) (bool, error) {
	key := paneString(pane)
	if key == "" || pane.GetPaneId() == "" {
		return false, fmt.Errorf("%w", ErrPane)
	}
	if err := checkID(goal); err != nil {
		return false, err
	}
	r.mu.Lock()
	_, replaced := r.byPane[key]
	entry := &agentv1.GoalRegistration{
		Goal: &typev1.GoalId{Value: goal}, Pane: pane,
		Harness:      &typev1.Harness{Name: goalfile.Clean(harness, 32)},
		WorktreePath: worktreePath,
		Source:       agentv1.GoalBindingSource_GOAL_BINDING_SOURCE_REGISTRY,
		RegisterTime: timestamppb.New(r.now()), RepoDriver: repoDriver,
	}
	r.byPane[key] = entry
	r.mu.Unlock()
	kind := agentv1.ChangeKind_CHANGE_KIND_CREATED
	if replaced {
		kind = agentv1.ChangeKind_CHANGE_KIND_UPDATED
	}
	if err := r.changed(context.Background(), kind, entry); err != nil {
		return replaced, err
	}
	return replaced, nil
}

// Rebuild restores one entry from FLEET_GOAL_ID in a window the fleet
// created (source ENV, 02h "State loss"). It behaves like Register but
// keeps the ENV source.
func (r *Registry) Rebuild(pane *typev1.PaneKey, goal, harness, worktreePath string) error {
	key := paneString(pane)
	if key == "" || pane.GetPaneId() == "" {
		return fmt.Errorf("%w", ErrPane)
	}
	if err := checkID(goal); err != nil {
		return err
	}
	r.mu.Lock()
	entry := &agentv1.GoalRegistration{
		Goal: &typev1.GoalId{Value: goal}, Pane: pane,
		Harness:      &typev1.Harness{Name: goalfile.Clean(harness, 32)},
		WorktreePath: worktreePath,
		Source:       agentv1.GoalBindingSource_GOAL_BINDING_SOURCE_ENV,
		RegisterTime: timestamppb.New(r.now()),
	}
	r.byPane[key] = entry
	r.mu.Unlock()
	return r.changed(context.Background(), agentv1.ChangeKind_CHANGE_KIND_CREATED, entry)
}

// Release removes pane's entry when it names goal and reports whether
// it removed anything (declarative: an absent or mismatched entry is
// OK and removes nothing, so a ReleaseGoal that lost a race never drops
// a newer goal). A removal emits GoalRegistryChanged CLOSED carrying
// the entry as it was.
func (r *Registry) Release(pane *typev1.PaneKey, goal string) (bool, error) {
	key := paneString(pane)
	r.mu.Lock()
	entry, ok := r.byPane[key]
	if !ok || entry.GetGoal().GetValue() != goal {
		r.mu.Unlock()
		return false, nil
	}
	delete(r.byPane, key)
	r.mu.Unlock()
	if err := r.changed(context.Background(), agentv1.ChangeKind_CHANGE_KIND_CLOSED, entry); err != nil {
		return true, err
	}
	return true, nil
}

// ReleaseByPane removes whatever entry names pane (pane exit,
// PruneWorktree) and reports whether it removed anything.
func (r *Registry) ReleaseByPane(pane *typev1.PaneKey) (bool, error) {
	key := paneString(pane)
	r.mu.Lock()
	entry, ok := r.byPane[key]
	if !ok {
		r.mu.Unlock()
		return false, nil
	}
	delete(r.byPane, key)
	r.mu.Unlock()
	if err := r.changed(context.Background(), agentv1.ChangeKind_CHANGE_KIND_CLOSED, entry); err != nil {
		return true, err
	}
	return true, nil
}

// LearnSession records the harness session id the first hook after
// registration carried (E9) and reports whether it learned anything.
// Harness session ids are untrusted hook text: capped and cleaned at
// this boundary.
func (r *Registry) LearnSession(pane *typev1.PaneKey, session string) (bool, error) {
	session = goalfile.Clean(session, 256)
	if session == "" {
		return false, nil
	}
	key := paneString(pane)
	r.mu.Lock()
	entry, ok := r.byPane[key]
	if !ok {
		r.mu.Unlock()
		return false, fmt.Errorf("goalregistry: no entry for the pane")
	}
	if entry.GetHarnessSessionId() == session {
		r.mu.Unlock()
		return false, nil
	}
	entry.HarnessSessionId = session
	r.mu.Unlock()
	return true, nil
}

// ByPane returns the pane's entry, if any.
func (r *Registry) ByPane(pane *typev1.PaneKey) (*agentv1.GoalRegistration, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.byPane[paneString(pane)]
	return entry, ok
}

// BySession returns the entry of one harness whose learned session id
// matches, if any. The harness must agree with the caller (hook.py only
// binds when harness and session both match), so a session id reused
// across harnesses never binds the wrong goal.
func (r *Registry) BySession(session, harness string) (*agentv1.GoalRegistration, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, entry := range r.byPane {
		if session != "" && entry.GetHarnessSessionId() == session && entry.GetHarness().GetName() == harness {
			return entry, true
		}
	}
	return nil, false
}

// Entries lists every entry, ordered by goal id.
func (r *Registry) Entries() []*agentv1.GoalRegistration {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*agentv1.GoalRegistration, 0, len(r.byPane))
	for _, entry := range r.byPane {
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GetGoal().GetValue() < out[j].GetGoal().GetValue() })
	return out
}

// FillSnapshot carries the whole registry for the hub's level-triggered
// reconcile (02h).
func (r *Registry) FillSnapshot(snap *agentv1.Snapshot) {
	snap.GoalRegistry = r.Entries()
}

// changed emits one GoalRegistryChanged. The caller already committed
// the change; an emit error is reported but the change stands (the next
// snapshot carries the level).
func (r *Registry) changed(ctx context.Context, kind agentv1.ChangeKind, entry *agentv1.GoalRegistration) error {
	if r.emit == nil {
		return nil
	}
	_, err := r.emit(ctx, &agentv1.Event{Body: &agentv1.Event_GoalRegistry{
		GoalRegistry: &agentv1.GoalRegistryChanged{Kind: kind, Registration: entry},
	}})
	return err
}
