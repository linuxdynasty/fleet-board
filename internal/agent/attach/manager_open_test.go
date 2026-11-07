package attach

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// TestManagerOpenMismatchRejects: a re-open that changes the command (mode,
// size or target) fails closed instead of joining the wrong session; the
// original attach keeps serving.
func TestManagerOpenMismatchRejects(t *testing.T) {
	t.Parallel()
	program, argvFile := fakeTmux(t)
	streams := map[string]*fakeStream{}
	m := testManager(t, program, streams)
	defer m.DropAll()
	ctx := context.WithValue(context.Background(), streamKey{}, "re")
	first, err := m.Open(ctx, openCmd("re", "work", 80, 24, false))
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	other := openCmd("re", "other", 80, 24, false)
	viewer := openCmd("re", "work", 80, 24, true)
	wide := openCmd("re", "work", 100, 24, false)
	tall := openCmd("re", "work", 80, 30, false)
	for _, tc := range []struct {
		name string
		cmd  *agentv1.OpenAttach
	}{
		{"other target", other},
		{"added read-only", viewer},
		{"wider", wide},
		{"taller", tall},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := m.Open(ctx, tc.cmd); !errors.Is(err, ErrInvalid) {
				t.Fatalf("mismatched reopen = %v, want ErrInvalid", err)
			}
		})
	}
	// The original still serves: an identical re-open returns its view and
	// no second view was ever made.
	again, err := m.Open(ctx, openCmd("re", "work", 80, 24, false))
	if err != nil || again != first {
		t.Fatalf("identical reopen = %q, %v, want %q, nil", again, err, first)
	}
	n := 0
	for _, line := range argvLines(t, argvFile) {
		if strings.Contains(line, "new-session") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("new-session ran %d times, want 1", n)
	}
	if ids := m.OpenIDs(); len(ids) != 1 || ids[0] != "re" {
		t.Fatalf("OpenIDs = %q, want [re]", ids)
	}
}

// TestPaneSelectScopedToView: a pane select runs select-pane against the
// view (session:window.pane), never a bare server-wide pane id; a pane the
// view does not hold runs no select-pane at all.
func TestPaneSelectScopedToView(t *testing.T) {
	t.Parallel()
	program, argvFile := fakeTmux(t)
	streams := map[string]*fakeStream{}
	m := testManager(t, program, streams)
	defer m.DropAll()
	ctx := context.WithValue(context.Background(), streamKey{}, "sel")
	if _, err := m.Open(ctx, openCmd("sel", "work", 80, 24, false)); err != nil {
		t.Fatalf("Open: %v", err)
	}
	st := streams["sel"]
	pane := func(id string) func(*agentv1.TermDown) {
		return func(d *agentv1.TermDown) {
			d.Body = &agentv1.TermDown_Select{Select: &agentv1.TermSelect{
				Target: &agentv1.TermSelect_Pane{Pane: &typev1.PaneKey{Server: "default", ServerStartId: "start-1", PaneId: id}},
			}}
		}
	}
	st.recv <- down("sel", pane("%9"))
	st.recv <- down("sel", pane("%99"))
	waitFor(t, "scoped select", 10*time.Second, func() bool {
		for _, line := range argvLines(t, argvFile) {
			if strings.Contains(line, "select-pane") {
				return true
			}
		}
		return false
	})
	time.Sleep(500 * time.Millisecond)
	n := 0
	for _, line := range argvLines(t, argvFile) {
		if !strings.Contains(line, "select-pane") {
			continue
		}
		n++
		if !strings.Contains(line, "fleet-view-sel:0.%9") {
			t.Fatalf("select-pane argv = %q, want it scoped to fleet-view-sel:0.%%9", line)
		}
	}
	if n != 1 {
		t.Fatalf("select-pane ran %d times, want 1 (the foreign pane runs none)", n)
	}
}
