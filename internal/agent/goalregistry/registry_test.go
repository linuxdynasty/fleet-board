package goalregistry

import (
	"context"
	"testing"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

type emitted struct {
	kind string
	reg  *agentv1.GoalRegistration
}

type emitter struct {
	evs []emitted
	err error
}

func (e *emitter) emit(_ context.Context, ev *agentv1.Event) (uint64, error) {
	if e.err != nil {
		return 0, e.err
	}
	g := ev.GetGoalRegistry()
	e.evs = append(e.evs, emitted{kind: g.GetKind().String(), reg: g.GetRegistration()})
	return uint64(len(e.evs)), nil
}

func testRegistry(em *emitter) *Registry {
	now := time.Unix(1_700_000_000, 0)
	return New(em.emit, func() time.Time { return now })
}

func pane(server, start, id string) *typev1.PaneKey {
	return &typev1.PaneKey{Server: server, ServerStartId: start, PaneId: id}
}

func TestRegisterAndLookup(t *testing.T) {
	t.Parallel()
	em := &emitter{}
	r := testRegistry(em)
	replaced, err := r.Register(pane("default", "s1", "%1"), "01abc", "claude", "/wt/one", false)
	if err != nil || replaced {
		t.Fatalf("Register = %v, %v; want false, nil", replaced, err)
	}
	got, ok := r.ByPane(pane("default", "s1", "%1"))
	if !ok || got.GetGoal().GetValue() != "01abc" || got.GetWorktreePath() != "/wt/one" {
		t.Fatalf("ByPane = %+v, %v", got, ok)
	}
	if got.GetSource() != agentv1.GoalBindingSource_GOAL_BINDING_SOURCE_REGISTRY {
		t.Fatalf("source = %s", got.GetSource())
	}
	if len(em.evs) != 1 || em.evs[0].kind != "CHANGE_KIND_CREATED" {
		t.Fatalf("emitted = %+v, want one CREATED", em.evs)
	}
	// A second goal in the same pane replaces the first.
	replaced, err = r.Register(pane("default", "s1", "%1"), "01def", "claude", "/wt/two", false)
	if err != nil || !replaced {
		t.Fatalf("re-Register = %v, %v; want true, nil", replaced, err)
	}
	got, _ = r.ByPane(pane("default", "s1", "%1"))
	if got.GetGoal().GetValue() != "01def" {
		t.Fatalf("after replace: %+v", got)
	}
}

func TestRegisterRefusesBadPane(t *testing.T) {
	t.Parallel()
	r := testRegistry(&emitter{})
	if _, err := r.Register(nil, "01abc", "claude", "/wt", false); err == nil {
		t.Fatalf("nil pane accepted")
	}
	if _, err := r.Register(pane("default", "s1", ""), "01abc", "claude", "/wt", false); err == nil {
		t.Fatalf("empty pane id accepted")
	}
	if _, err := r.Register(pane("default", "s1", "%1"), "UPPER!", "claude", "/wt", false); err == nil {
		t.Fatalf("bad goal id accepted")
	}
}

func TestRelease(t *testing.T) {
	t.Parallel()
	em := &emitter{}
	r := testRegistry(em)
	p := pane("default", "s1", "%1")
	if _, err := r.Register(p, "01abc", "codex", "/wt", false); err != nil {
		t.Fatal(err)
	}
	// Wrong goal id for the pane removes nothing.
	if removed, err := r.Release(p, "01zzz"); err != nil || removed {
		t.Fatalf("mismatched release = %v, %v", removed, err)
	}
	if _, ok := r.ByPane(p); !ok {
		t.Fatalf("mismatched release dropped the entry")
	}
	if removed, err := r.Release(p, "01abc"); err != nil || !removed {
		t.Fatalf("release = %v, %v", removed, err)
	}
	if _, ok := r.ByPane(p); ok {
		t.Fatalf("entry still there after release")
	}
	// Releasing an absent entry is OK.
	if removed, err := r.Release(p, "01abc"); err != nil || removed {
		t.Fatalf("absent release = %v, %v", removed, err)
	}
	last := em.evs[len(em.evs)-1]
	if last.kind != "CHANGE_KIND_CLOSED" || last.reg.GetGoal().GetValue() != "01abc" {
		t.Fatalf("last event = %+v, want CLOSED of 01abc", last)
	}
}

func TestLearnSession(t *testing.T) {
	t.Parallel()
	r := testRegistry(&emitter{})
	p := pane("default", "s1", "%1")
	if _, err := r.Register(p, "01abc", "muse", "/wt", false); err != nil {
		t.Fatal(err)
	}
	changed, err := r.LearnSession(p, "sess-9")
	if err != nil || !changed {
		t.Fatalf("LearnSession = %v, %v", changed, err)
	}
	same, err := r.LearnSession(p, "sess-9")
	if err != nil || same {
		t.Fatalf("second LearnSession = %v, %v", same, err)
	}
	got, ok := r.BySession("sess-9", "muse")
	if !ok || got.GetGoal().GetValue() != "01abc" {
		t.Fatalf("BySession = %+v, %v", got, ok)
	}
	if _, err := r.LearnSession(pane("default", "s1", "%9"), "sess-9"); err == nil {
		t.Fatalf("learn on unregistered pane accepted")
	}
}

func TestFillSnapshot(t *testing.T) {
	t.Parallel()
	r := testRegistry(&emitter{})
	if _, err := r.Register(pane("default", "s1", "%2"), "01b", "claude", "/wt/b", false); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Register(pane("default", "s1", "%1"), "01a", "codex", "/wt/a", true); err != nil {
		t.Fatal(err)
	}
	snap := &agentv1.Snapshot{}
	r.FillSnapshot(snap)
	got := snap.GetGoalRegistry()
	if len(got) != 2 || got[0].GetGoal().GetValue() != "01a" || got[1].GetGoal().GetValue() != "01b" {
		t.Fatalf("snapshot registry order/content: %+v", got)
	}
	if !got[0].GetRepoDriver() || got[1].GetRepoDriver() {
		t.Fatalf("repo_driver lost: %+v", got)
	}
}

// TestBySessionScopesHarness: a harness session id binds only the entry
// whose harness agrees with the caller (02h). Two harnesses reusing one
// session id must not resolve to each other's goal.
func TestBySessionScopesHarness(t *testing.T) {
	t.Parallel()
	r := testRegistry(&emitter{})
	p1 := pane("default", "s1", "%1")
	p2 := pane("default", "s1", "%2")
	if _, err := r.Register(p1, "01a", "claude", "/wt", false); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Register(p2, "01b", "codex", "/wt", false); err != nil {
		t.Fatal(err)
	}
	if _, err := r.LearnSession(p1, "sess-x"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.LearnSession(p2, "sess-x"); err != nil {
		t.Fatal(err)
	}
	got, ok := r.BySession("sess-x", "codex")
	if !ok || got.GetGoal().GetValue() != "01b" {
		t.Fatalf("BySession(sess-x, codex) = %+v, %v, want 01b", got, ok)
	}
	got, ok = r.BySession("sess-x", "claude")
	if !ok || got.GetGoal().GetValue() != "01a" {
		t.Fatalf("BySession(sess-x, claude) = %+v, %v, want 01a", got, ok)
	}
	if _, ok := r.BySession("sess-x", "muse"); ok {
		t.Fatal("BySession(sess-x, muse) matched, want silent")
	}
}
