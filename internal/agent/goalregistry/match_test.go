package goalregistry

import (
	"testing"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
)

func seed(t *testing.T) *Registry {
	t.Helper()
	r := testRegistry(&emitter{})
	if _, err := r.Register(pane("default", "s1", "%1"), "01aaa", "claude", "/wt/one", false); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Register(pane("default", "s1", "%2"), "01bbb", "muse", "/wt/two", false); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestResolve(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		call      Call
		wantGoal  string
		wantLearn bool
		preLearn  bool
		silent    bool
	}{
		"session match wins over pane": {
			call:     Call{Harness: "claude", Kind: agentv1.HookEventKind_HOOK_EVENT_KIND_USER_PROMPT, Pane: pane("default", "s9", "%9"), Session: "sess-live"},
			wantGoal: "01aaa",
			preLearn: true,
		},
		"pane match learns session": {
			call:      Call{Harness: "claude", Kind: agentv1.HookEventKind_HOOK_EVENT_KIND_USER_PROMPT, Pane: pane("default", "s1", "%1"), Session: "sess-new"},
			wantGoal:  "01aaa",
			wantLearn: true,
		},
		"pane match without session learns nothing": {
			call:     Call{Harness: "codex", Kind: agentv1.HookEventKind_HOOK_EVENT_KIND_USER_PROMPT, Pane: pane("default", "s1", "%1")},
			wantGoal: "01aaa",
		},
		"muse cwd single pane learns": {
			call:      Call{Harness: "muse", Kind: agentv1.HookEventKind_HOOK_EVENT_KIND_USER_PROMPT, Session: "muse-sess", Cwd: "/wt/two"},
			wantGoal:  "01bbb",
			wantLearn: true,
		},
		"muse cwd under worktree": {
			call:      Call{Harness: "muse", Kind: agentv1.HookEventKind_HOOK_EVENT_KIND_USER_PROMPT, Session: "muse-sess", Cwd: "/wt/two/sub/dir"},
			wantGoal:  "01bbb",
			wantLearn: true,
		},
		"muse sibling path is silent": {
			call:   Call{Harness: "muse", Kind: agentv1.HookEventKind_HOOK_EVENT_KIND_USER_PROMPT, Session: "muse-sess", Cwd: "/wt/two-elsewhere"},
			silent: true,
		},
		"session start stays inert": {
			call:   Call{Harness: "claude", Kind: agentv1.HookEventKind_HOOK_EVENT_KIND_SESSION_START, Pane: pane("default", "s1", "%1"), Session: "sess-new"},
			silent: true,
		},
		"unknown pane is silent": {
			call:   Call{Harness: "claude", Kind: agentv1.HookEventKind_HOOK_EVENT_KIND_TURN_END, Pane: pane("default", "s1", "%7")},
			silent: true,
		},
		"no pane no session no cwd is silent": {
			call:   Call{Harness: "muse", Kind: agentv1.HookEventKind_HOOK_EVENT_KIND_USER_PROMPT},
			silent: true,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := seed(t)
			if tc.preLearn {
				if _, err := r.LearnSession(pane("default", "s1", "%1"), "sess-live"); err != nil {
					t.Fatal(err)
				}
			}
			out := r.Resolve(tc.call)
			if tc.silent {
				if !out.Silent || out.Entry != nil {
					t.Fatalf("Resolve = %+v, want silent", out)
				}
				return
			}
			if out.Silent || out.Entry == nil {
				t.Fatalf("Resolve silent, want %s", tc.wantGoal)
			}
			if got := out.Entry.GetGoal().GetValue(); got != tc.wantGoal {
				t.Fatalf("goal = %s, want %s", got, tc.wantGoal)
			}
			if out.LearnedSession != tc.wantLearn {
				t.Fatalf("learned = %v, want %v", out.LearnedSession, tc.wantLearn)
			}
		})
	}
}

func TestResolveMuseAmbiguity(t *testing.T) {
	t.Parallel()
	r := testRegistry(&emitter{})
	if _, err := r.Register(pane("default", "s1", "%1"), "01aaa", "muse", "/wt/one", false); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Register(pane("default", "s1", "%2"), "01bbb", "muse", "/wt/one", false); err != nil {
		t.Fatal(err)
	}
	out := r.Resolve(Call{Harness: "muse", Kind: agentv1.HookEventKind_HOOK_EVENT_KIND_USER_PROMPT, Session: "s", Cwd: "/wt/one"})
	if !out.Silent {
		t.Fatalf("two Muse panes one checkout matched %+v, want silent", out.Entry)
	}
}

func TestResolveMuseDriverAndLearned(t *testing.T) {
	t.Parallel()
	r := testRegistry(&emitter{})
	if _, err := r.Register(pane("default", "s1", "%1"), "01aaa", "muse", "/main/checkout", true); err != nil {
		t.Fatal(err)
	}
	// REPO_DRIVER entries never match by cwd.
	out := r.Resolve(Call{Harness: "muse", Kind: agentv1.HookEventKind_HOOK_EVENT_KIND_USER_PROMPT, Session: "s", Cwd: "/main/checkout"})
	if !out.Silent {
		t.Fatalf("driver entry matched by cwd: %+v", out.Entry)
	}
	// A learned entry matches its own session, and a new session stays silent.
	if _, err := r.LearnSession(pane("default", "s1", "%1"), "old-sess"); err != nil {
		t.Fatal(err)
	}
	out = r.Resolve(Call{Harness: "muse", Kind: agentv1.HookEventKind_HOOK_EVENT_KIND_USER_PROMPT, Session: "old-sess", Cwd: "/elsewhere"})
	if out.Silent || out.Entry.GetGoal().GetValue() != "01aaa" || out.LearnedSession {
		t.Fatalf("learned session should match without re-learn: %+v", out)
	}
	out = r.Resolve(Call{Harness: "muse", Kind: agentv1.HookEventKind_HOOK_EVENT_KIND_USER_PROMPT, Session: "new-sess", Cwd: "/main/checkout"})
	if !out.Silent {
		t.Fatalf("new session on a learned driver entry matched: %+v", out.Entry)
	}
}

// TestResolveSessionCollisionAcrossHarnesses: one session id learned on
// two harnesses binds each call to its own harness's goal, never the
// other harness's (hook.py requires the same agreement).
func TestResolveSessionCollisionAcrossHarnesses(t *testing.T) {
	t.Parallel()
	r := seed(t)
	if _, err := r.LearnSession(pane("default", "s1", "%1"), "sess-both"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.LearnSession(pane("default", "s1", "%2"), "sess-both"); err != nil {
		t.Fatal(err)
	}
	out := r.Resolve(Call{Harness: "muse", Kind: agentv1.HookEventKind_HOOK_EVENT_KIND_USER_PROMPT,
		Pane: pane("other", "s9", "%9"), Session: "sess-both"})
	if out.Silent || out.Entry.GetGoal().GetValue() != "01bbb" {
		t.Fatalf("muse resolve = %+v, want 01bbb", out)
	}
	out = r.Resolve(Call{Harness: "claude", Kind: agentv1.HookEventKind_HOOK_EVENT_KIND_USER_PROMPT,
		Pane: pane("other", "s9", "%9"), Session: "sess-both"})
	if out.Silent || out.Entry.GetGoal().GetValue() != "01aaa" {
		t.Fatalf("claude resolve = %+v, want 01aaa", out)
	}
}
