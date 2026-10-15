package control

import (
	"strings"
	"testing"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/exec"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux/tmuxtest"
)

func windowCmd(name string) *agentv1.EnsureWindow {
	return &agentv1.EnsureWindow{
		Session:    &agentv1.SessionTarget{By: &agentv1.SessionTarget_Name{Name: "control-base"}},
		WindowName: name,
	}
}

// TestWindowHandlerContract pins the executor seam for F2: the kind the
// handler runs, the capability it advertises for Command.requires, and the
// restart class (EnsureWindow is declarative, so a resume re-runs it from
// the start). F2 appears here as a whole word for the capability report.
func TestWindowHandlerContract(t *testing.T) {
	t.Parallel()
	e, err := New(testOptions())
	if err != nil {
		t.Fatal(err)
	}
	h := e.WindowHandler()
	if h.Kind() != "EnsureWindow" {
		t.Fatalf("Kind = %q, want EnsureWindow (F2)", h.Kind())
	}
	caps := h.Capabilities()
	// 02e "Command kinds and capabilities": tmux.control plus the
	// server-specific one. F2 appears here as a whole word for the
	// capability report.
	want := map[string]uint32{"tmux.control": 1, "tmux.default_server.control": 1}
	if len(caps) != len(want) {
		t.Fatalf("Capabilities = %v, want %v for F2", caps, want)
	}
	for _, c := range caps {
		if want[c.GetName()] != c.GetVersion() || c.GetVersion() != 1 {
			t.Fatalf("Capabilities = %v, want %v for F2", caps, want)
		}
	}
	cmd := &agentv1.Command{Kind: &agentv1.Command_EnsureWindow{EnsureWindow: &agentv1.EnsureWindow{}}}
	if got := h.Class(cmd); got != exec.RepeatSafe {
		t.Fatalf("Class = %v, want RepeatSafe for F2", got)
	}
}

func TestEnsureWindowRefusals(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		change func(*agentv1.EnsureWindow)
		status typev1.Status
		reason typev1.ReasonCode
	}{
		{"empty window name", func(c *agentv1.EnsureWindow) { c.WindowName = "" }, typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_NAME},
		{"newline window name", func(c *agentv1.EnsureWindow) { c.WindowName = "fleet\nwin" }, typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_NAME},
		{"oversized window name", func(c *agentv1.EnsureWindow) {
			c.WindowName = strings.Repeat("w", 257)
		}, typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_NAME},
		{"no session target", func(c *agentv1.EnsureWindow) { c.Session = nil }, typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT},
		{"shapeless target name", func(c *agentv1.EnsureWindow) {
			c.Session = &agentv1.SessionTarget{By: &agentv1.SessionTarget_Name{Name: "NOT A NAME"}}
		}, typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_NAME},
		{"shapeless target session id", func(c *agentv1.EnsureWindow) {
			c.Session = &agentv1.SessionTarget{By: &agentv1.SessionTarget_SessionId{SessionId: &typev1.SessionId{Value: "NOT AN ID"}}}
		}, typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT},
		{"tmux id without a start id", func(c *agentv1.EnsureWindow) {
			c.Session = &agentv1.SessionTarget{By: &agentv1.SessionTarget_TmuxId{TmuxId: "$0"}}
		}, typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT},
		{"tmux id of the wrong shape", func(c *agentv1.EnsureWindow) {
			c.Session = &agentv1.SessionTarget{
				By:            &agentv1.SessionTarget_TmuxId{TmuxId: "control-base"},
				ServerStartId: "0-0",
			}
		}, typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT},
		{"harness waits for its slice", func(c *agentv1.EnsureWindow) {
			c.Harness = &typev1.Harness{Name: "claude"}
		}, typev1.Status_STATUS_UNSUPPORTED, typev1.ReasonCode_REASON_CODE_HARNESS_NOT_ALLOWED},
		{"resume waits for its slice", func(c *agentv1.EnsureWindow) { c.Resume = true }, typev1.Status_STATUS_UNSUPPORTED, typev1.ReasonCode_REASON_CODE_HARNESS_NOT_ALLOWED},
		{"profile without a harness", func(c *agentv1.EnsureWindow) { c.Profile = "work" }, typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT},
		{"wrong server kind", func(c *agentv1.EnsureWindow) {
			c.Session = &agentv1.SessionTarget{
				Server: typev1.TmuxServerKind_TMUX_SERVER_KIND_PROBE,
				By:     &agentv1.SessionTarget_Name{Name: "control-base"},
			}
		}, typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT},
		{"shapeless goal id", func(c *agentv1.EnsureWindow) {
			c.GoalId = &typev1.GoalId{Value: "NOT AN ID"}
		}, typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e, err := New(testOptions())
			if err != nil {
				t.Fatal(err)
			}
			cmd := windowCmd("fleet-win")
			tc.change(cmd)
			res := e.EnsureWindow(t.Context(), cmd)
			if res.GetStatus() != tc.status || res.GetReason() != tc.reason {
				t.Fatalf("status/reason = %v/%v, want %v/%v (%s)",
					res.GetStatus(), res.GetReason(), tc.status, tc.reason, res.GetMessage())
			}
			if res.GetDetail() != nil {
				t.Fatalf("refusal carries a detail: %v", res.GetDetail())
			}
		})
	}
}

// TestEnsureWindowCreatesAndReuses runs F2 against real tmux: a new name
// creates a window tagged for the fleet, the same name with reuse attaches
// to it, and without reuse it is refused unless the stored goal tag matches
// the retried goal (the crash window, as in F1).
func TestEnsureWindowCreatesAndReuses(t *testing.T) {
	t.Parallel()
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			t.Parallel()
			o, srv := liveOptions(t, b, []string{"/tmp"})
			e, err := New(o)
			if err != nil {
				t.Fatal(err)
			}
			ctx := t.Context()

			create := windowCmd("fleet-win")
			create.GoalId = &typev1.GoalId{Value: "goal-9"}
			res := e.EnsureWindow(ctx, create)
			if res.GetStatus() != typev1.Status_STATUS_OK {
				t.Fatalf("create status/reason = %v/%v (%s)", res.GetStatus(), res.GetReason(), res.GetMessage())
			}
			got := res.GetEnsureWindow()
			if got == nil || !got.GetCreated() {
				t.Fatalf("create detail = %v, want created=true", got)
			}
			key := got.GetPane()
			if key.GetServer() != "default" || key.GetPaneId() == "" || key.GetServerStartId() == "" {
				t.Fatalf("pane key = %v, want server/default, a pane id and a start id", key)
			}
			win := got.GetWindow()
			if win.GetKey().GetWindowId() == "" || win.GetName() != "fleet-win" {
				t.Fatalf("window = %v, want a window id and name fleet-win", win)
			}
			if win.GetKey().GetServerStartId() != key.GetServerStartId() {
				t.Fatalf("window start id = %v, pane start id = %v, want both from this server",
					win.GetKey().GetServerStartId(), key.GetServerStartId())
			}
			// The tags the observer reads: the @fleet_* window options,
			// including the first pane's key and the goal id, and
			// automatic-rename off for fleet windows (01f).
			paneKey := "default/" + key.GetServerStartId() + "/" + key.GetPaneId()
			for _, tc := range []struct {
				args []string
				want string
			}{
				{[]string{"show-options", "-w", "-v", "-t", win.GetKey().GetWindowId(), "@fleet_pane_key"}, paneKey + "\n"},
				{[]string{"show-options", "-w", "-v", "-t", win.GetKey().GetWindowId(), "@fleet_goal_id"}, "goal-9\n"},
				{[]string{"show-options", "-w", "-v", "-t", win.GetKey().GetWindowId(), "automatic-rename"}, "off\n"},
			} {
				if out := srv.Run(t, tc.args...); out != tc.want {
					t.Fatalf("%v = %q, want %q", tc.args, out, tc.want)
				}
			}
			listed := srv.Run(t, "list-windows", "-t", "control-base", "-F", "#{window_name}")
			if !strings.Contains(listed, "fleet-win") {
				t.Fatalf("list-windows = %q, want fleet-win", listed)
			}

			again := e.EnsureWindow(ctx, windowCmd("fleet-win"))
			if again.GetStatus() != typev1.Status_STATUS_FAILED || again.GetReason() != typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT {
				t.Fatalf("exists without reuse = %v/%v, want FAILED/INVALID_ARGUMENT",
					again.GetStatus(), again.GetReason())
			}

			reuse := windowCmd("fleet-win")
			reuse.Reuse = true
			reused := e.EnsureWindow(ctx, reuse)
			if reused.GetStatus() != typev1.Status_STATUS_OK || reused.GetEnsureWindow().GetCreated() {
				t.Fatalf("reuse = %v, want OK with created=false", reused)
			}
			if reused.GetEnsureWindow().GetWindow().GetKey().GetWindowId() != win.GetKey().GetWindowId() {
				t.Fatalf("reuse window id = %v, want %v",
					reused.GetEnsureWindow().GetWindow().GetKey().GetWindowId(), win.GetKey().GetWindowId())
			}

			// The crash window: the first call created the window but its
			// result never reached the log, so the executor re-runs the
			// RepeatSafe handler with reuse=false. The stored goal tag
			// matches, so the retry completes OK instead of failing on a
			// window that is already the command's own effect.
			retry := windowCmd("fleet-win")
			retry.GoalId = &typev1.GoalId{Value: "goal-9"}
			if res := e.EnsureWindow(ctx, retry); res.GetStatus() != typev1.Status_STATUS_OK || res.GetEnsureWindow().GetCreated() {
				t.Fatalf("retry status/created = %v/%v, want OK with created=false",
					res.GetStatus(), res.GetEnsureWindow().GetCreated())
			}

			// Another goal on the same name is a real conflict.
			clash := windowCmd("fleet-win")
			clash.GoalId = &typev1.GoalId{Value: "goal-other"}
			if res := e.EnsureWindow(ctx, clash); res.GetStatus() != typev1.Status_STATUS_FAILED || res.GetReason() != typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT {
				t.Fatalf("clash status/reason = %v/%v, want FAILED/INVALID_ARGUMENT",
					res.GetStatus(), res.GetReason())
			}
		})
	}
}

// TestEnsureWindowSessionTarget runs F2 against real tmux through every
// session target form: a fleet name, the hub session id stored on it, and
// the tmux id guarded by the server start id.
func TestEnsureWindowSessionTarget(t *testing.T) {
	t.Parallel()
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			t.Parallel()
			o, srv := liveOptions(t, b, []string{"/tmp"})
			e, err := New(o)
			if err != nil {
				t.Fatal(err)
			}
			ctx := t.Context()

			sess := ensureCmd("fleet-wsess")
			sess.SessionId = &typev1.SessionId{Value: "sess-w"}
			if res := e.EnsureSession(ctx, sess); res.GetStatus() != typev1.Status_STATUS_OK {
				t.Fatalf("session create status/reason = %v/%v (%s)", res.GetStatus(), res.GetReason(), res.GetMessage())
			}

			byID := &agentv1.EnsureWindow{
				Session:    &agentv1.SessionTarget{By: &agentv1.SessionTarget_SessionId{SessionId: &typev1.SessionId{Value: "sess-w"}}},
				WindowName: "by-id",
			}
			if res := e.EnsureWindow(ctx, byID); res.GetStatus() != typev1.Status_STATUS_OK {
				t.Fatalf("by session id status/reason = %v/%v (%s)", res.GetStatus(), res.GetReason(), res.GetMessage())
			}

			unknown := &agentv1.EnsureWindow{
				Session:    &agentv1.SessionTarget{By: &agentv1.SessionTarget_SessionId{SessionId: &typev1.SessionId{Value: "sess-nope"}}},
				WindowName: "by-id",
			}
			if res := e.EnsureWindow(ctx, unknown); res.GetStatus() != typev1.Status_STATUS_FAILED || res.GetReason() != typev1.ReasonCode_REASON_CODE_SESSION_NOT_FOUND {
				t.Fatalf("unknown session id = %v/%v, want FAILED/SESSION_NOT_FOUND",
					res.GetStatus(), res.GetReason())
			}

			tmuxID := strings.TrimSpace(srv.Run(t, "display-message", "-t", "fleet-wsess", "-p", "#{session_id}"))
			start, err := e.startID(ctx)
			if err != nil {
				t.Fatal(err)
			}
			byTmux := &agentv1.EnsureWindow{
				Session: &agentv1.SessionTarget{
					By:            &agentv1.SessionTarget_TmuxId{TmuxId: tmuxID},
					ServerStartId: start,
				},
				WindowName: "by-tmux",
			}
			if res := e.EnsureWindow(ctx, byTmux); res.GetStatus() != typev1.Status_STATUS_OK {
				t.Fatalf("by tmux id status/reason = %v/%v (%s)", res.GetStatus(), res.GetReason(), res.GetMessage())
			}

			stale := &agentv1.EnsureWindow{
				Session: &agentv1.SessionTarget{
					By:            &agentv1.SessionTarget_TmuxId{TmuxId: tmuxID},
					ServerStartId: "0-0",
				},
				WindowName: "by-tmux",
			}
			if res := e.EnsureWindow(ctx, stale); res.GetStatus() != typev1.Status_STATUS_FAILED || res.GetReason() != typev1.ReasonCode_REASON_CODE_SESSION_NOT_FOUND {
				t.Fatalf("stale start id = %v/%v, want FAILED/SESSION_NOT_FOUND",
					res.GetStatus(), res.GetReason())
			}

			missing := windowCmd("fleet-win")
			missing.Session = &agentv1.SessionTarget{By: &agentv1.SessionTarget_Name{Name: "no-such-session"}}
			if res := e.EnsureWindow(ctx, missing); res.GetStatus() != typev1.Status_STATUS_FAILED || res.GetReason() != typev1.ReasonCode_REASON_CODE_SESSION_NOT_FOUND {
				t.Fatalf("missing session = %v/%v, want FAILED/SESSION_NOT_FOUND",
					res.GetStatus(), res.GetReason())
			}
		})
	}
}

// TestEnsureWindowRacers runs two creates for the same new name at once
// against real tmux: the per-server lock serializes the list/create
// sequence, so exactly one window lands and the loser takes the conflict
// instead of doubling under the cap.
func TestEnsureWindowRacers(t *testing.T) {
	t.Parallel()
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			t.Parallel()
			o, srv := liveOptions(t, b, []string{"/tmp"})
			e, err := New(o)
			if err != nil {
				t.Fatal(err)
			}
			ctx := t.Context()
			start := make(chan struct{})
			results := make(chan *agentv1.CommandResult, 2)
			for range 2 {
				go func() {
					<-start
					results <- e.EnsureWindow(ctx, windowCmd("fleet-race"))
				}()
			}
			close(start)
			var created, conflict int
			for range 2 {
				res := <-results
				switch {
				case res.GetStatus() == typev1.Status_STATUS_OK && res.GetEnsureWindow().GetCreated():
					created++
				case res.GetStatus() == typev1.Status_STATUS_FAILED && res.GetReason() == typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT:
					conflict++
				default:
					t.Errorf("racer = %v/%v, want one create and one conflict",
						res.GetStatus(), res.GetReason())
				}
			}
			if created != 1 || conflict != 1 {
				t.Fatalf("created/conflict = %d/%d, want 1/1", created, conflict)
			}
			listed := srv.Run(t, "list-windows", "-t", "control-base", "-F", "#{window_name}")
			if n := strings.Count(listed, "fleet-race"); n != 1 {
				t.Fatalf("windows named fleet-race = %d, want 1", n)
			}
		})
	}
}

// TestEnsureWindowLimits pins the 02h failure reasons that need live tmux:
// a session already at its window cap refuses with MAX_WINDOWS_REACHED, and
// a ".." work path is refused outright like F1.
func TestEnsureWindowLimits(t *testing.T) {
	t.Parallel()
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			t.Parallel()
			o, _ := liveOptions(t, b, []string{"/tmp"})
			o.MaxWindows = func() uint32 { return 1 }
			e, err := New(o)
			if err != nil {
				t.Fatal(err)
			}
			// control-base already holds one window, so any create is
			// over the cap of one.
			if res := e.EnsureWindow(t.Context(), windowCmd("fleet-capped")); res.GetStatus() != typev1.Status_STATUS_FAILED || res.GetReason() != typev1.ReasonCode_REASON_CODE_MAX_WINDOWS_REACHED {
				t.Fatalf("over cap = %v/%v, want FAILED/MAX_WINDOWS_REACHED",
					res.GetStatus(), res.GetReason())
			}
		})
	}

	t.Run("traversal refused", func(t *testing.T) {
		t.Parallel()
		o := testOptions()
		o.Roots = func() []string { return []string{"/"} }
		o.ResolveWorkdir = func(w *typev1.Workdir) (string, error) { return "/tmp/fleet-control-repo", nil }
		e, err := New(o)
		if err != nil {
			t.Fatal(err)
		}
		cmd := windowCmd("fleet-trav")
		cmd.Workdir = &typev1.Workdir{RelativePath: "../secret"}
		res := e.EnsureWindow(t.Context(), cmd)
		if res.GetStatus() != typev1.Status_STATUS_REJECTED_BY_POLICY || res.GetReason() != typev1.ReasonCode_REASON_CODE_REPO_ROOT_NOT_ALLOWED {
			t.Fatalf("status/reason = %v/%v (%s), want REJECTED_BY_POLICY/REPO_ROOT_NOT_ALLOWED",
				res.GetStatus(), res.GetReason(), res.GetMessage())
		}
	})
}
