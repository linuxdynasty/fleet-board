package control

import (
	"strings"
	"testing"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/exec"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux/tmuxtest"
)

// liveKillEnsurer builds an Ensurer against a throwaway server running
// `exec sleep 60` and returns it with the server and the server's start
// id. Every behavior below runs against real tmux. KillWindow never
// types, so a stub sender proves nothing was typed.
func liveKillEnsurer(t *testing.T, b tmuxtest.Binary) (*Ensurer, *tmuxtest.Server, string) {
	t.Helper()
	srv := tmuxtest.Start(t, b, "-s", "work", "-x", "120", "-y", "20", "exec sleep 60")
	o := testOptions()
	o.Socket = srv.Socket
	if b.Path != "" {
		o.Tmux = b.Path
	}
	e, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	start, err := e.startID(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return e, srv, start
}

// liveWindowIDs lists the server's window ids.
func liveWindowIDs(t *testing.T, srv *tmuxtest.Server) []string {
	t.Helper()
	out := srv.Run(t, "list-windows", "-a", "-F", "#{window_id}")
	var ids []string
	for line := range strings.Lines(out) {
		if id := strings.TrimSpace(line); id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

// liveWindowNamed returns the id of the named window.
func liveWindowNamed(t *testing.T, srv *tmuxtest.Server, name string) string {
	t.Helper()
	out := srv.Run(t, "list-windows", "-a", "-F", "#{window_id} #{window_name}")
	for line := range strings.Lines(strings.TrimSpace(out)) {
		id, got, _ := strings.Cut(line, " ")
		if got == name {
			return id
		}
	}
	t.Fatalf("no window named %q in %q", name, out)
	return ""
}

func liveKillCmd(id, start string) *agentv1.KillWindow {
	return &agentv1.KillWindow{Window: killKey(start, id)}
}

func TestKillLiveMissingAndStale(t *testing.T) {
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			e, srv, start := liveKillEnsurer(t, b)
			id := liveWindowIDs(t, srv)[0]
			for _, tc := range []struct {
				name string
				cmd  *agentv1.KillWindow
			}{
				{"missing window", liveKillCmd("@99", start)},
				{"stale server start", liveKillCmd(id, "0-0")},
			} {
				t.Run(tc.name, func(t *testing.T) {
					res := e.KillWindow(t.Context(), tc.cmd, false, typev1.Role_ROLE_OWNER)
					if res.GetStatus() != typev1.Status_STATUS_FAILED ||
						res.GetReason() != typev1.ReasonCode_REASON_CODE_WINDOW_NOT_FOUND {
						t.Fatalf("KillWindow = (%v, %v), want (FAILED, WINDOW_NOT_FOUND): %q",
							res.GetStatus(), res.GetReason(), res.GetMessage())
					}
				})
			}
		})
	}
}

// TestKillLiveFleetWindowKilled is the happy path on real tmux: a window
// in a session carrying the @fleet_session_id tag F1 writes reads the
// fleet_window variant and is killed, and the window is gone afterwards.
func TestKillLiveFleetWindowKilled(t *testing.T) {
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			e, srv, start := liveKillEnsurer(t, b)
			srv.Run(t, "set-option", "-t", "work", "@fleet_session_id", "sess-live")
			srv.Run(t, "new-window", "-d", "-t", "work", "-n", "fleetwin", "exec sleep 60")
			id := liveWindowNamed(t, srv, "fleetwin")
			h := e.KillWindowHandler()
			v, ok := h.(exec.Variant)
			if !ok {
				t.Fatal("KillWindow handler does not answer the role-matrix variant")
			}
			cmd := &agentv1.Command{Kind: &agentv1.Command_KillWindow{KillWindow: liveKillCmd(id, start)}}
			if got := v.Variant(t.Context(), cmd); got != "fleet_window" {
				t.Fatalf("Variant = %q, want fleet_window for a tagged session's window", got)
			}
			res := e.KillWindow(t.Context(), liveKillCmd(id, start), true, typev1.Role_ROLE_SCHEDULER)
			if res.GetStatus() != typev1.Status_STATUS_OK {
				t.Fatalf("KillWindow = (%v, %v), want OK: %q",
					res.GetStatus(), res.GetReason(), res.GetMessage())
			}
			for _, got := range liveWindowIDs(t, srv) {
				if got == id {
					t.Fatalf("window %s still listed after the kill", id)
				}
			}
		})
	}
}

// TestKillLiveDeveloperWindowGate is the destructive default-deny proof
// on real tmux: an untagged window reads the developer_window variant,
// the scheduler's fleet cell is refused ROLE_INSUFFICIENT with the
// window still alive — nothing was killed — and the same window dies
// once the matrix (an owner) allowed it.
func TestKillLiveDeveloperWindowGate(t *testing.T) {
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			e, srv, start := liveKillEnsurer(t, b)
			srv.Run(t, "new-window", "-d", "-t", "work", "-n", "devwin", "exec sleep 60")
			id := liveWindowNamed(t, srv, "devwin")
			h := e.KillWindowHandler()
			v, ok := h.(exec.Variant)
			if !ok {
				t.Fatal("KillWindow handler does not answer the role-matrix variant")
			}
			cmd := &agentv1.Command{Kind: &agentv1.Command_KillWindow{KillWindow: liveKillCmd(id, start)}}
			if got := v.Variant(t.Context(), cmd); got != "developer_window" {
				t.Fatalf("Variant = %q, want developer_window for an untagged window", got)
			}
			res := e.KillWindow(t.Context(), liveKillCmd(id, start), true, typev1.Role_ROLE_SCHEDULER)
			if res.GetStatus() != typev1.Status_STATUS_REJECTED_BY_POLICY ||
				res.GetReason() != typev1.ReasonCode_REASON_CODE_ROLE_INSUFFICIENT {
				t.Fatalf("KillWindow = (%v, %v), want (REJECTED_BY_POLICY, ROLE_INSUFFICIENT): %q",
					res.GetStatus(), res.GetReason(), res.GetMessage())
			}
			found := false
			for _, got := range liveWindowIDs(t, srv) {
				if got == id {
					found = true
				}
			}
			if !found {
				t.Fatalf("window %s gone after a refused kill", id)
			}
			// An operator ask admitted as a fleet window before a tag
			// removal meets the same refusal at kill time.
			res = e.KillWindow(t.Context(), liveKillCmd(id, start), false, typev1.Role_ROLE_OPERATOR)
			if res.GetStatus() != typev1.Status_STATUS_REJECTED_BY_POLICY ||
				res.GetReason() != typev1.ReasonCode_REASON_CODE_ROLE_INSUFFICIENT {
				t.Fatalf("KillWindow = (%v, %v), want (REJECTED_BY_POLICY, ROLE_INSUFFICIENT): %q",
					res.GetStatus(), res.GetReason(), res.GetMessage())
			}
			res = e.KillWindow(t.Context(), liveKillCmd(id, start), false, typev1.Role_ROLE_OWNER)
			if res.GetStatus() != typev1.Status_STATUS_OK {
				t.Fatalf("KillWindow = (%v, %v), want OK: %q",
					res.GetStatus(), res.GetReason(), res.GetMessage())
			}
			for _, got := range liveWindowIDs(t, srv) {
				if got == id {
					t.Fatalf("window %s still listed after the kill", id)
				}
			}
		})
	}
}
