package attach

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/tmux/tmuxtest"
	"github.com/linuxdynasty/fleet-board/internal/proc"
)

// liveViews builds Views against a throwaway server running a shell in
// session work.
func liveViews(t *testing.T, b tmuxtest.Binary) (*Views, *tmuxtest.Server) {
	t.Helper()
	tmuxtest.Need(t, b)
	srv := tmuxtest.Start(t, b, "-s", "work", "-x", "120", "-y", "30")
	caller, err := proc.For("tmux")
	if err != nil {
		t.Fatal(err)
	}
	runner, err := proc.NewRunner(nil)
	if err != nil {
		t.Fatal(err)
	}
	program := "tmux"
	if b.Path != "" {
		program = b.Path
	}
	return &Views{
		Program: program, Runner: runner, Caller: caller,
		ServerStart: func(string) (string, bool) { return "start-1", true },
	}, srv
}

// TestViewsCreateView: the view groups with the target, survives with no
// client attached yet (it is not armed), and leaves the target untouched.
func TestViewsCreateView(t *testing.T) {
	t.Parallel()
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			t.Parallel()
			v, srv := liveViews(t, b)
			ctx := context.Background()
			view := "fleet-view-aaaabbbbccccddddeeeeffff00"
			if err := v.CreateView(ctx, srv.Socket, "work", view, 80, 24); err != nil {
				t.Fatalf("CreateView: %v", err)
			}
			if got := strings.TrimSpace(srv.Run(t, "list-sessions", "-F", "#{session_name}")); !strings.Contains(got, view) {
				t.Fatalf("sessions = %q, want the unarmed view to survive", got)
			}
			workWindows := strings.TrimSpace(srv.Run(t, "list-windows", "-t", "work", "-F", "#{window_id}"))
			viewWindows := strings.TrimSpace(srv.Run(t, "list-windows", "-t", view, "-F", "#{window_id}"))
			if viewWindows != workWindows {
				t.Fatalf("view windows = %q, want the target's %q (grouped)", viewWindows, workWindows)
			}
		})
	}
}

// TestViewsArmKill: arming a live view is accepted, killing a live view
// removes it while the target keeps running, and killing a view that is
// already gone succeeds.
func TestViewsArmKill(t *testing.T) {
	t.Parallel()
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			t.Parallel()
			v, srv := liveViews(t, b)
			ctx := context.Background()
			view := "fleet-view-aaaabbbbccccddddeeeeffff00"
			if err := v.CreateView(ctx, srv.Socket, "work", view, 80, 24); err != nil {
				t.Fatalf("CreateView: %v", err)
			}
			if err := v.ArmDestroy(ctx, srv.Socket, view); err != nil {
				t.Fatalf("ArmDestroy: %v", err)
			}
			if err := v.KillView(ctx, srv.Socket, view); err != nil {
				t.Fatalf("KillView: %v", err)
			}
			if got := strings.TrimSpace(srv.Run(t, "list-sessions", "-F", "#{session_name}")); strings.Contains(got, view) {
				t.Fatalf("sessions = %q, want the view gone", got)
			}
			if got := strings.TrimSpace(srv.Run(t, "list-sessions", "-F", "#{session_name}")); !strings.Contains(got, "work") {
				t.Fatalf("sessions = %q, want the target still running", got)
			}
			if err := v.KillView(ctx, srv.Socket, view); err != nil {
				t.Fatalf("KillView of a gone view: %v", err)
			}
			if err := v.ArmDestroy(ctx, srv.Socket, "view-x"); err == nil {
				t.Fatal("ArmDestroy of a bad view name succeeded, want refusal")
			}
			if err := v.KillView(ctx, srv.Socket, "view-x"); err == nil {
				t.Fatal("KillView of a bad view name succeeded, want refusal")
			}
		})
	}
}

// TestViewsCreateRefusals: bad names, sizes and a missing target fail
// before (or without) touching tmux.
func TestViewsCreateRefusals(t *testing.T) {
	t.Parallel()
	v := &Views{ServerStart: func(string) (string, bool) { return "s", true }}
	ctx := context.Background()
	for _, tc := range []struct {
		name, target, view string
		cols, rows         int
	}{
		{"upper target", "Work", "fleet-view-aaaabbbbccccddddeeeeffff00", 80, 24},
		{"target traversal", "../x", "fleet-view-aaaabbbbccccddddeeeeffff00", 80, 24},
		{"view prefix", "work", "view-aaaabbbbccccddddeeeeffff00", 80, 24},
		{"view traversal", "work", "fleet-view-../..", 80, 24},
		{"zero cols", "work", "fleet-view-aaaabbbbccccddddeeeeffff00", 0, 24},
		{"huge rows", "work", "fleet-view-aaaabbbbccccddddeeeeffff00", 80, 1001},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if err := v.CreateView(ctx, "nosocket", tc.target, tc.view, tc.cols, tc.rows); err == nil {
				t.Fatal("CreateView succeeded, want refusal")
			}
		})
	}
}

// TestViewsMissingTarget: a target that is gone fails instead of making
// an ungrouped view.
func TestViewsMissingTarget(t *testing.T) {
	t.Parallel()
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			t.Parallel()
			v, srv := liveViews(t, b)
			if err := v.CreateView(context.Background(), srv.Socket, "gone",
				"fleet-view-aaaabbbbccccddddeeeeffff00", 80, 24); err == nil {
				t.Fatal("CreateView on a missing target succeeded")
			}
			if out := strings.TrimSpace(srv.Run(t, "list-sessions", "-F", "#{session_name}")); strings.Contains(out, "fleet-view-") {
				t.Fatalf("sessions = %q, want no leftover view", out)
			}
		})
	}
}

// TestViewsWaitAttached: a bad view name is refused at once, and a live
// view with no client yet times out instead of waiting forever. A landed
// client is covered where the manager opens one.
func TestViewsWaitAttached(t *testing.T) {
	t.Parallel()
	if err := (&Views{}).WaitAttached(context.Background(), "nosocket", "view-x", time.Second); err == nil {
		t.Fatal("WaitAttached on a bad view name succeeded, want refusal")
	}
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			t.Parallel()
			v, srv := liveViews(t, b)
			ctx := context.Background()
			view := "fleet-view-aaaabbbbccccddddeeeeffff00"
			if err := v.CreateView(ctx, srv.Socket, "work", view, 80, 24); err != nil {
				t.Fatalf("CreateView: %v", err)
			}
			if err := v.WaitAttached(ctx, srv.Socket, view, 300*time.Millisecond); err == nil {
				t.Fatal("WaitAttached with no client succeeded, want a timeout")
			}
		})
	}
}

// TestViewsSelectWindow: selecting by window id moves the view's current
// window and never the target's.
func TestViewsSelectWindow(t *testing.T) {
	t.Parallel()
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			t.Parallel()
			v, srv := liveViews(t, b)
			ctx := context.Background()
			view := "fleet-view-aaaabbbbccccddddeeeeffff00"
			if err := v.CreateView(ctx, srv.Socket, "work", view, 80, 24); err != nil {
				t.Fatalf("CreateView: %v", err)
			}
			srv.Run(t, "new-window", "-t", "work", "-n", "second")
			ids := strings.Fields(strings.TrimSpace(srv.Run(t, "list-windows", "-t", view, "-F", "#{window_id}")))
			if len(ids) != 2 {
				t.Fatalf("windows = %q, want 2", ids)
			}
			var second string
			for _, id := range ids {
				first := strings.TrimSpace(srv.Run(t, "display-message", "-p", "-t", view, "#{window_id}"))
				if id != first {
					second = id
				}
			}
			// Park the target on the first window: new-window left it
			// on the second, so only a parked target proves the view
			// selects without moving it.
			srv.Run(t, "select-window", "-t", "work:0")
			workParked := strings.TrimSpace(srv.Run(t, "display-message", "-p", "-t", "work", "#{window_id}"))
			if err := v.SelectWindow(ctx, srv.Socket, "default", "start-1", view, second); err != nil {
				t.Fatalf("SelectWindow: %v", err)
			}
			if got := strings.TrimSpace(srv.Run(t, "display-message", "-p", "-t", view, "#{window_id}")); got != second {
				t.Fatalf("view current = %q, want %q", got, second)
			}
			if got := strings.TrimSpace(srv.Run(t, "display-message", "-p", "-t", "work", "#{window_id}")); got != workParked {
				t.Fatalf("target current = %q, want parked %q", got, workParked)
			}
			if err := v.SelectWindow(ctx, srv.Socket, "default", "stale", view, second); err == nil {
				t.Fatal("stale server start selected, want refusal")
			}
			if err := v.SelectWindow(ctx, srv.Socket, "default", "start-1", view, "@999"); err == nil {
				t.Fatal("unknown window selected, want refusal")
			}
		})
	}
}

// TestViewsSelectPane: selecting by pane id moves the view's active pane;
// a pane outside the attached session is refused.
func TestViewsSelectPane(t *testing.T) {
	t.Parallel()
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			t.Parallel()
			v, srv := liveViews(t, b)
			ctx := context.Background()
			view := "fleet-view-aaaabbbbccccddddeeeeffff00"
			if err := v.CreateView(ctx, srv.Socket, "work", view, 80, 24); err != nil {
				t.Fatalf("CreateView: %v", err)
			}
			srv.Run(t, "split-window", "-t", "work")
			panes := strings.Fields(strings.TrimSpace(srv.Run(t, "list-panes", "-t", view, "-F", "#{pane_id}")))
			if len(panes) != 2 {
				t.Fatalf("panes = %q, want 2", panes)
			}
			var other string
			for _, p := range panes {
				first := strings.TrimSpace(srv.Run(t, "display-message", "-p", "-t", view, "#{pane_id}"))
				if p != first {
					other = p
				}
			}
			if err := v.SelectPane(ctx, srv.Socket, "default", "start-1", view, other); err != nil {
				t.Fatalf("SelectPane: %v", err)
			}
			if got := strings.TrimSpace(srv.Run(t, "display-message", "-p", "-t", view, "#{pane_id}")); got != other {
				t.Fatalf("view active pane = %q, want %q", got, other)
			}
			srv.NewSession(t, "-s", "other")
			foreign := strings.TrimSpace(srv.Run(t, "list-panes", "-t", "other", "-F", "#{pane_id}"))
			if err := v.SelectPane(ctx, srv.Socket, "default", "start-1", view, foreign); err == nil {
				t.Fatal("foreign pane selected, want refusal")
			}
			if err := v.SelectPane(ctx, srv.Socket, "default", "stale", view, other); err == nil {
				t.Fatal("stale server start selected, want refusal")
			}
		})
	}
}
