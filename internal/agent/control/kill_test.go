package control

import (
	"context"
	"errors"
	"strings"
	"testing"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/exec"
	"github.com/linuxdynasty/fleet-board/internal/proc"
)

// killCmd builds a KillWindow for window @5. The shape refusals below
// fail before any tmux call, so the dummy socket in testOptions never
// answers.
func killCmd() *agentv1.KillWindow {
	return &agentv1.KillWindow{
		Window: &typev1.WindowKey{
			Server:        "default",
			ServerStartId: "live-start-id",
			WindowId:      "@5",
		},
	}
}

func TestKillRefusesABadShape(t *testing.T) {
	t.Parallel()
	e, err := New(testOptions())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		mute func(*agentv1.KillWindow)
	}{
		{"no window", func(c *agentv1.KillWindow) { c.Window = nil }},
		{"another server", func(c *agentv1.KillWindow) {
			c.Window = &typev1.WindowKey{Server: "fleet", ServerStartId: "live-start-id", WindowId: "@5"}
		}},
		{"pane id is not a window", func(c *agentv1.KillWindow) {
			c.Window = &typev1.WindowKey{Server: "default", ServerStartId: "live-start-id", WindowId: "%1"}
		}},
		{"session id is not a window", func(c *agentv1.KillWindow) {
			c.Window = &typev1.WindowKey{Server: "default", ServerStartId: "live-start-id", WindowId: "$3"}
		}},
		{"bare window id", func(c *agentv1.KillWindow) {
			c.Window = &typev1.WindowKey{Server: "default", ServerStartId: "live-start-id", WindowId: "5"}
		}},
		{"window id with a name", func(c *agentv1.KillWindow) {
			c.Window = &typev1.WindowKey{Server: "default", ServerStartId: "live-start-id", WindowId: "@work"}
		}},
		{"no server start id", func(c *agentv1.KillWindow) {
			c.Window = &typev1.WindowKey{Server: "default", WindowId: "@5"}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cmd := killCmd()
			tc.mute(cmd)
			res := e.KillWindow(t.Context(), cmd, false, typev1.Role_ROLE_OWNER)
			if res.GetStatus() != typev1.Status_STATUS_FAILED ||
				res.GetReason() != typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT {
				t.Fatalf("KillWindow = (%v, %v), want (FAILED, INVALID_ARGUMENT): %q",
					res.GetStatus(), res.GetReason(), res.GetMessage())
			}
		})
	}
}

// TestKillHandlerContract pins the executor seam for F6: the kind the
// handler runs, the capability it advertises for Command.requires, and
// the restart class (02e: KillWindow is repeat safe, so a resume re-runs
// it from the start). F6 appears here as a whole word for the capability
// report.
func TestKillHandlerContract(t *testing.T) {
	t.Parallel()
	e, err := New(testOptions())
	if err != nil {
		t.Fatal(err)
	}
	h := e.KillWindowHandler()
	if h.Kind() != "KillWindow" {
		t.Fatalf("Kind = %q, want KillWindow (F6)", h.Kind())
	}
	caps := h.Capabilities()
	// 02e "Command kinds and capabilities": tmux.control plus the
	// server-specific one. F6 appears here as a whole word for the
	// capability report.
	want := map[string]uint32{"tmux.control": 1, "tmux.default_server.control": 1}
	if len(caps) != len(want) {
		t.Fatalf("Capabilities = %v, want %v for F6", caps, want)
	}
	for _, c := range caps {
		if want[c.GetName()] != c.GetVersion() || c.GetVersion() != 1 {
			t.Fatalf("Capabilities = %v, want %v for F6", caps, want)
		}
	}
	cmd := &agentv1.Command{Kind: &agentv1.Command_KillWindow{KillWindow: &agentv1.KillWindow{}}}
	if got := h.Class(cmd); got != exec.RepeatSafe {
		t.Fatalf("Class = %v, want RepeatSafe for F6", got)
	}
	// RepeatSafe resumes by re-running: a shapeless command is refused
	// again, never parked as unknown.
	bad := &agentv1.Command{Kind: &agentv1.Command_KillWindow{KillWindow: &agentv1.KillWindow{}}}
	res := h.Resume(t.Context(), exec.Request{Command: bad})
	if res.GetStatus() != typev1.Status_STATUS_FAILED ||
		res.GetReason() != typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT {
		t.Fatalf("Resume = (%v, %v), want (FAILED, INVALID_ARGUMENT): %q",
			res.GetStatus(), res.GetReason(), res.GetMessage())
	}
	// A handler for another kind never runs here.
	other := &agentv1.Command{Kind: &agentv1.Command_Interrupt{Interrupt: &agentv1.Interrupt{}}}
	res = h.Run(t.Context(), exec.Request{Command: other})
	if res.GetStatus() != typev1.Status_STATUS_FAILED ||
		res.GetReason() != typev1.ReasonCode_REASON_CODE_INTERNAL {
		t.Fatalf("Run(other) = (%v, %v), want (FAILED, INTERNAL): %q",
			res.GetStatus(), res.GetReason(), res.GetMessage())
	}
}

// TestKillRunRechecksTheRole pins the admission re-check through the
// handler seam: Run carries the command's own role, so an operator ask
// for a window whose fleet tag is gone is refused with nothing killed,
// while an owner ask for the same window runs through.
func TestKillRunRechecksTheRole(t *testing.T) {
	t.Parallel()
	start := startID(1000, 2000)
	for _, tc := range []struct {
		name   string
		role   typev1.Role
		wantOK bool
	}{
		{"operator refused", typev1.Role_ROLE_OPERATOR, false},
		{"owner runs", typev1.Role_ROLE_OWNER, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			o := testOptions()
			r := killScript(false)
			o.Runner = r
			e, err := New(o)
			if err != nil {
				t.Fatal(err)
			}
			h := e.KillWindowHandler()
			cmd := &agentv1.Command{
				Kind:        &agentv1.Command_KillWindow{KillWindow: &agentv1.KillWindow{Window: killKey(start, "@5")}},
				RequestedBy: &typev1.Principal{Role: tc.role},
			}
			res := h.Run(t.Context(), exec.Request{Command: cmd})
			if tc.wantOK {
				if res.GetStatus() != typev1.Status_STATUS_OK {
					t.Fatalf("Run = (%v, %v), want OK: %q",
						res.GetStatus(), res.GetReason(), res.GetMessage())
				}
				if r.kills != 1 {
					t.Fatalf("kill-window attempted %d times, want one", r.kills)
				}
				return
			}
			if res.GetStatus() != typev1.Status_STATUS_REJECTED_BY_POLICY ||
				res.GetReason() != typev1.ReasonCode_REASON_CODE_ROLE_INSUFFICIENT {
				t.Fatalf("Run = (%v, %v), want (REJECTED_BY_POLICY, ROLE_INSUFFICIENT): %q",
					res.GetStatus(), res.GetReason(), res.GetMessage())
			}
			if r.kills != 0 {
				t.Fatalf("kill-window attempted %d times on a refused kill, want none", r.kills)
			}
		})
	}
}

// killRunner answers each tmux call from its script, keyed by the full
// argv past "-S <socket>" (tag reads need per-target answers, which a
// subcommand-only script cannot give). It counts kill-window calls, so a
// refused kill proves nothing was killed, not just a mapped refusal.
type killRunner struct {
	out   map[string]string
	err   map[string]error
	kills int
}

func (s *killRunner) Run(_ context.Context, sp proc.Spec) (proc.Result, error) {
	key := strings.Join(sp.Args[2:], " ")
	if strings.HasPrefix(key, "kill-window ") {
		s.kills++
	}
	if err, ok := s.err[key]; ok {
		return proc.Result{}, err
	}
	return proc.Result{Stdout: []byte(s.out[key])}, nil
}

// killScript is the fleet session "work" holding window @5: the
// display-message generation, the server-wide window listing, and the
// tag reads. fleetTags switches the @fleet_session_id answers between a
// fleet window and a developer one.
func killScript(fleetTags bool) *killRunner {
	tags := ""
	if fleetTags {
		tags = "sess-1"
	}
	return &killRunner{out: map[string]string{
		"display-message -p #{pid} #{start_time}":         "1000 2000",
		"list-windows -a -F #{window_id} #{session_name}": "@5 0 work",
		"show-options -w -v -t @5 @fleet_session_id":      tags,
		"show-options -v -t work @fleet_session_id":       tags,
		"kill-window -t @5":                               "",
	}}
}

func killKey(start string, id string) *typev1.WindowKey {
	return &typev1.WindowKey{Server: "default", ServerStartId: start, WindowId: id}
}

// TestKillWindowChecks pins the F4-modeled generation and listing checks
// on a scripted runner: a stale start id and a missing window read
// WINDOW_NOT_FOUND before any kill is attempted.
func TestKillWindowChecks(t *testing.T) {
	t.Parallel()
	start := startID(1000, 2000)
	for _, tc := range []struct {
		name   string
		key    *typev1.WindowKey
		reason typev1.ReasonCode
	}{
		{"stale server start", killKey("0-0", "@5"), typev1.ReasonCode_REASON_CODE_WINDOW_NOT_FOUND},
		{"missing window", killKey(start, "@99"), typev1.ReasonCode_REASON_CODE_WINDOW_NOT_FOUND},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			o := testOptions()
			r := killScript(false)
			o.Runner = r
			e, err := New(o)
			if err != nil {
				t.Fatal(err)
			}
			res := e.KillWindow(t.Context(), &agentv1.KillWindow{Window: tc.key}, false, typev1.Role_ROLE_OWNER)
			if res.GetStatus() != typev1.Status_STATUS_FAILED || res.GetReason() != tc.reason {
				t.Fatalf("KillWindow = (%v, %v), want (FAILED, %v): %q",
					res.GetStatus(), res.GetReason(), tc.reason, res.GetMessage())
			}
			if r.kills != 0 {
				t.Fatalf("kill-window attempted %d times on a refused kill, want none", r.kills)
			}
		})
	}
}

// TestKillFleetGate pins the 02g F check the executor hands down, plus
// the admission re-check at kill time: a non-owner kill runs only while
// the fleet's tags still mark the window, so a tag removed between the
// role check and the kill (across a local-approval wait) is refused
// ROLE_INSUFFICIENT instead of killing under the wrong row. Only an
// owner kills an unmarked window.
func TestKillFleetGate(t *testing.T) {
	t.Parallel()
	start := startID(1000, 2000)
	for _, tc := range []struct {
		name      string
		fleetTags bool
		fleetOnly bool
		role      typev1.Role
		wantOK    bool
		want      typev1.ReasonCode
	}{
		{"fleet window with the fleet cell kills", true, true, typev1.Role_ROLE_SCHEDULER, true, typev1.ReasonCode_REASON_CODE_UNSPECIFIED},
		{"fleet window without the fleet cell kills", true, false, typev1.Role_ROLE_OPERATOR, true, typev1.ReasonCode_REASON_CODE_UNSPECIFIED},
		{"owner kills a developer window", false, false, typev1.Role_ROLE_OWNER, true, typev1.ReasonCode_REASON_CODE_UNSPECIFIED},
		{"fleet cell on a developer window is refused", false, true, typev1.Role_ROLE_SCHEDULER, false, typev1.ReasonCode_REASON_CODE_ROLE_INSUFFICIENT},
		{"operator re-check on a developer window is refused", false, false, typev1.Role_ROLE_OPERATOR, false, typev1.ReasonCode_REASON_CODE_ROLE_INSUFFICIENT},
		{"unknown role on a developer window is refused", false, false, typev1.Role_ROLE_UNSPECIFIED, false, typev1.ReasonCode_REASON_CODE_ROLE_INSUFFICIENT},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			o := testOptions()
			r := killScript(tc.fleetTags)
			o.Runner = r
			e, err := New(o)
			if err != nil {
				t.Fatal(err)
			}
			res := e.KillWindow(t.Context(), &agentv1.KillWindow{Window: killKey(start, "@5")}, tc.fleetOnly, tc.role)
			if tc.wantOK {
				if res.GetStatus() != typev1.Status_STATUS_OK {
					t.Fatalf("KillWindow = (%v, %v), want OK: %q",
						res.GetStatus(), res.GetReason(), res.GetMessage())
				}
				if r.kills != 1 {
					t.Fatalf("kill-window attempted %d times, want one", r.kills)
				}
				return
			}
			if res.GetStatus() != typev1.Status_STATUS_REJECTED_BY_POLICY || res.GetReason() != tc.want {
				t.Fatalf("KillWindow = (%v, %v), want (REJECTED_BY_POLICY, %v): %q",
					res.GetStatus(), res.GetReason(), tc.want, res.GetMessage())
			}
			if r.kills != 0 {
				t.Fatalf("kill-window attempted %d times on a refused kill, want none", r.kills)
			}
		})
	}
}

// TestKillVariantPlacesWindows pins the role-matrix variant (02g): the
// handler answers fleet_window only for a window the fleet's tags mark,
// and developer_window — the strictest row — for anything else,
// including a server that answers nothing.
func TestKillVariantPlacesWindows(t *testing.T) {
	t.Parallel()
	start := startID(1000, 2000)
	t.Run("fleet window", func(t *testing.T) {
		t.Parallel()
		o := testOptions()
		o.Runner = killScript(true)
		e, err := New(o)
		if err != nil {
			t.Fatal(err)
		}
		h := e.KillWindowHandler()
		v, ok := h.(exec.Variant)
		if !ok {
			t.Fatal("KillWindow handler does not answer the role-matrix variant")
		}
		cmd := &agentv1.Command{Kind: &agentv1.Command_KillWindow{
			KillWindow: &agentv1.KillWindow{Window: killKey(start, "@5")},
		}}
		if got := v.Variant(t.Context(), cmd); got != "fleet_window" {
			t.Fatalf("Variant = %q, want fleet_window", got)
		}
	})
	for _, tc := range []struct {
		name string
		key  *typev1.WindowKey
		down bool
	}{
		{"developer window", killKey(start, "@5"), false},
		{"stale server start", killKey("0-0", "@5"), false},
		{"missing window", killKey(start, "@99"), false},
		{"bad window id", killKey(start, "5"), false},
		{"tmux down", killKey(start, "@5"), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			o := testOptions()
			r := killScript(false)
			if tc.down {
				r.err = map[string]error{
					"display-message -p #{pid} #{start_time}": errors.New("no server running on /tmp/dead"),
				}
			}
			o.Runner = r
			e, err := New(o)
			if err != nil {
				t.Fatal(err)
			}
			h := e.KillWindowHandler()
			v, ok := h.(exec.Variant)
			if !ok {
				t.Fatal("KillWindow handler does not answer the role-matrix variant")
			}
			cmd := &agentv1.Command{Kind: &agentv1.Command_KillWindow{
				KillWindow: &agentv1.KillWindow{Window: tc.key},
			}}
			if got := v.Variant(t.Context(), cmd); got != "developer_window" {
				t.Fatalf("Variant = %q, want developer_window", got)
			}
		})
	}
}
