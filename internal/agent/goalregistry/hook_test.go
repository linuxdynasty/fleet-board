package goalregistry

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/goalfile"
)

// stubBoardFor makes a bare .fleet-board under any workdir the registry
// names (the marker check is lane 1's; these tests prove the file layer
// above it).
func stubBoardFor(_ *testing.T, _ string) func(string) (string, bool, error) {
	return func(workdir string) (string, bool, error) {
		board := filepath.Join(workdir, ".fleet-board")
		if err := os.MkdirAll(board, 0o700); err != nil {
			return "", false, err
		}
		return board, true, nil
	}
}

type hookEmitted struct {
	goal     string
	kind     agentv1.HookEventKind
	decision agentv1.HookDecision
	e9       bool
}

type hookEmitter struct {
	evs []hookEmitted
}

func (e *hookEmitter) emit(_ context.Context, ev *agentv1.Event) (uint64, error) {
	if g := ev.GetGoalRegistry(); g != nil {
		return uint64(len(e.evs)), nil
	}
	if p := ev.GetPaneHarnessSession(); p != nil {
		e.evs = append(e.evs, hookEmitted{e9: true, goal: p.GetGoal().GetValue()})
		return uint64(len(e.evs)), nil
	}
	h := ev.GetHarnessHook()
	e.evs = append(e.evs, hookEmitted{goal: h.GetGoalId().GetValue(), kind: h.GetKind(), decision: h.GetDecision()})
	return uint64(len(e.evs)), nil
}

func hookSetup(t *testing.T, firstLine string) (*Registry, *hookEmitter) {
	t.Helper()
	em := &hookEmitter{}
	r := New(em.emit, nil)
	workdir := t.TempDir()
	files := goalfile.Writer{EnsureBoard: stubBoardFor(t, workdir)}
	if _, err := r.Register(pane("default", "s1", "%1"), "01aaa", "claude", workdir, false); err != nil {
		t.Fatal(err)
	}
	if _, err := files.EnsureGoalFile(workdir, "01aaa", []byte(firstLine+"\nCONSENT: push\n"), false); err != nil {
		t.Fatal(err)
	}
	return r, em
}

func TestTurnEndGuard(t *testing.T) {
	t.Parallel()
	alive := func(pid uint32) bool { return pid == 4242 }
	cases := map[string]struct {
		firstLine string
		decision  agentv1.HookDecision
	}{
		"done allows":                 {"DONE", agentv1.HookDecision_HOOK_DECISION_ALLOW},
		"working blocks":              {"rebuild the widget", agentv1.HookDecision_HOOK_DECISION_BLOCK},
		"live watch allows":           {"BLOCKED WATCH=4242", agentv1.HookDecision_HOOK_DECISION_ALLOW},
		"dead watch blocks":           {"BLOCKED WATCH=9999", agentv1.HookDecision_HOOK_DECISION_BLOCK},
		"owner and action allow":      {"BLOCKED OWNER=sam ACTION=approve plan", agentv1.HookDecision_HOOK_DECISION_ALLOW},
		"owner without action blocks": {"BLOCKED OWNER=sam", agentv1.HookDecision_HOOK_DECISION_BLOCK},
		"plain blocked blocks":        {"BLOCKED waiting on nothing", agentv1.HookDecision_HOOK_DECISION_BLOCK},
		"missing file allows":         {"", agentv1.HookDecision_HOOK_DECISION_ALLOW},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r, em := hookSetup(t, tc.firstLine)
			if tc.firstLine == "" {
				// Remove the file the setup wrote: a vanished goal file
				// (git clean -fdx) is not a guard refusal.
				workdir := r.Entries()[0].GetWorktreePath()
				if err := (goalfile.Writer{EnsureBoard: stubBoardFor(t, workdir)}).ReleaseGoalFile(workdir, "01aaa"); err != nil {
					t.Fatal(err)
				}
			}
			out, err := r.TurnEnd(context.Background(),
				Call{Harness: "claude", Kind: agentv1.HookEventKind_HOOK_EVENT_KIND_TURN_END, Pane: pane("default", "s1", "%1"), Session: "sess-1"},
				goalfile.Writer{EnsureBoard: stubBoardFor(t, r.Entries()[0].GetWorktreePath())}, alive)
			if err != nil {
				t.Fatal(err)
			}
			if out.Silent || out.Entry == nil {
				t.Fatalf("turn end went silent for a goal pane")
			}
			if len(em.evs) == 0 {
				t.Fatalf("no hook event emitted")
			}
			last := em.evs[len(em.evs)-1]
			if last.goal != "01aaa" || last.kind != agentv1.HookEventKind_HOOK_EVENT_KIND_TURN_END || last.decision != tc.decision {
				t.Fatalf("event = %+v, want goal 01aaa TURN_END %s", last, tc.decision)
			}
		})
	}
}

func TestTurnEndSilentAndKinds(t *testing.T) {
	t.Parallel()
	r, em := hookSetup(t, "DONE")
	// Unknown pane: silent, nothing emitted.
	out, err := r.TurnEnd(context.Background(),
		Call{Harness: "claude", Kind: agentv1.HookEventKind_HOOK_EVENT_KIND_TURN_END, Pane: pane("default", "s1", "%7")},
		goalfile.Writer{}, nil)
	if err != nil || !out.Silent {
		t.Fatalf("unmatched = %+v, %v; want silent", out, err)
	}
	// Interrupt of a goal pane arrives as E8 with no guard verdict.
	out, err = r.TurnEnd(context.Background(),
		Call{Harness: "codex", Kind: agentv1.HookEventKind_HOOK_EVENT_KIND_INTERRUPT, Pane: pane("default", "s1", "%1"), Session: "sess-1"},
		goalfile.Writer{}, nil)
	if err != nil || out.Silent {
		t.Fatalf("interrupt = %+v, %v", out, err)
	}
	if len(em.evs) != 2 || !em.evs[0].e9 || em.evs[0].goal != "01aaa" {
		t.Fatalf("interrupt should learn the session first (E9): %+v", em.evs)
	}
	last := em.evs[1]
	if last.kind != agentv1.HookEventKind_HOOK_EVENT_KIND_INTERRUPT || last.decision != agentv1.HookDecision_HOOK_DECISION_UNSPECIFIED {
		t.Fatalf("interrupt event = %+v", last)
	}
}
