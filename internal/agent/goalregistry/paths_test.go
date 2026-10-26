package goalregistry

import (
	"testing"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
)

func mustRegisterPath(t *testing.T, r *Registry, server, start, id, goal, wt string) {
	t.Helper()
	if _, err := r.Register(pane(server, start, id), goal, "claude", wt, false); err != nil {
		t.Fatalf("Register(%s) = %v", goal, err)
	}
}

func TestByGoal(t *testing.T) {
	t.Parallel()
	em := &emitter{}
	r := testRegistry(em)
	mustRegisterPath(t, r, "default", "s1", "%1", "01abc", "/wt/one")
	mustRegisterPath(t, r, "default", "s1", "%2", "02def", "/wt/two")

	cases := []struct {
		name string
		goal string
		want string
	}{
		{name: "first entry", goal: "01abc", want: "%1"},
		{name: "second entry", goal: "02def", want: "%2"},
		{name: "unknown goal", goal: "09zzz", want: ""},
		{name: "empty goal", goal: "", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := r.ByGoal(tc.goal)
			if tc.want == "" {
				if ok || got != nil {
					t.Fatalf("ByGoal(%q) = %+v, %v; want nil, false", tc.goal, got, ok)
				}
				return
			}
			if !ok || got.GetPane().GetPaneId() != tc.want {
				t.Fatalf("ByGoal(%q) = %+v, %v; want pane %s", tc.goal, got, ok, tc.want)
			}
		})
	}
}

func TestReleaseByPath(t *testing.T) {
	t.Parallel()
	em := &emitter{}
	r := testRegistry(em)
	mustRegisterPath(t, r, "default", "s1", "%1", "01abc", "/wt/one")
	mustRegisterPath(t, r, "default", "s1", "%2", "02def", "/wt/one/sub")
	mustRegisterPath(t, r, "default", "s1", "%3", "03ghi", "/wt/other")

	released := r.ReleaseByPath("/wt/one")
	if len(released) != 2 {
		t.Fatalf("ReleaseByPath = %d entries; want 2", len(released))
	}
	if _, ok := r.ByPane(pane("default", "s1", "%1")); ok {
		t.Fatal("pane %1 still bound after ReleaseByPath")
	}
	if _, ok := r.ByPane(pane("default", "s1", "%2")); ok {
		t.Fatal("pane %2 still bound after ReleaseByPath")
	}
	if _, ok := r.ByPane(pane("default", "s1", "%3")); !ok {
		t.Fatal("pane %3 dropped by an unrelated ReleaseByPath")
	}
	// Only the two removals emit CLOSED; the survivor emits nothing.
	closed := 0
	for _, ev := range em.evs {
		if ev.kind == agentv1.ChangeKind_CHANGE_KIND_CLOSED.String() {
			closed++
		}
	}
	if closed != 2 {
		t.Fatalf("closed events = %d; want 2", closed)
	}

	if got := r.ReleaseByPath("/wt/none"); len(got) != 0 {
		t.Fatalf("ReleaseByPath on an empty path = %d; want 0", len(got))
	}
	// A sibling prefix never matches: /wt/other is not under /wt/o.
	if got := r.ReleaseByPath("/wt/o"); len(got) != 0 {
		t.Fatalf("ReleaseByPath prefix sibling = %d; want 0", len(got))
	}
}
