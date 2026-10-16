package control

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux/tmuxtest"
)

// liveRespawnEnsurer builds an Ensurer against a throwaway server running
// `exec sleep 60` and returns it with the server, the first pane's id and
// the server's start id. Every behavior below runs against real tmux.
// Respawn needs no observer client: it never types, so a stub sender
// proves nothing was typed. remain-on-exit stays on so dead panes linger
// for the respawn.
func liveRespawnEnsurer(t *testing.T, b tmuxtest.Binary) (*Ensurer, *tmuxtest.Server, string, string) {
	t.Helper()
	srv := tmuxtest.Start(t, b, "-s", "work", "-x", "120", "-y", "20", "exec sleep 60")
	srv.Run(t, "set-window-option", "-g", "remain-on-exit", "on")
	o := testOptions()
	o.Socket = srv.Socket
	if b.Path != "" {
		o.Tmux = b.Path
	}
	e, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	pane := strings.TrimSpace(srv.Run(t, "list-panes", "-t", "work", "-F", "#{pane_id}"))
	start, err := e.startID(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return e, srv, pane, start
}

func liveRespawnCmd(pane, start string) *agentv1.RespawnPane {
	cmd := respawnCmd()
	cmd.Pane = &typev1.PaneKey{Server: "default", ServerStartId: start, PaneId: pane}
	return cmd
}

func TestRespawnLivePaneNotFound(t *testing.T) {
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			e, _, _, start := liveRespawnEnsurer(t, b)
			res := e.RespawnPane(t.Context(), liveRespawnCmd("%99", start))
			if res.GetStatus() != typev1.Status_STATUS_FAILED ||
				res.GetReason() != typev1.ReasonCode_REASON_CODE_PANE_NOT_FOUND {
				t.Fatalf("RespawnPane = (%v, %v), want (FAILED, PANE_NOT_FOUND): %q",
					res.GetStatus(), res.GetReason(), res.GetMessage())
			}
		})
	}
}

func TestRespawnLiveStaleStart(t *testing.T) {
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			e, _, pane, _ := liveRespawnEnsurer(t, b)
			res := e.RespawnPane(t.Context(), liveRespawnCmd(pane, "0-0"))
			if res.GetStatus() != typev1.Status_STATUS_FAILED ||
				res.GetReason() != typev1.ReasonCode_REASON_CODE_PANE_NOT_FOUND {
				t.Fatalf("RespawnPane = (%v, %v), want (FAILED, PANE_NOT_FOUND): %q",
					res.GetStatus(), res.GetReason(), res.GetMessage())
			}
		})
	}
}

// TestRespawnLivePaneAliveRefusedAndUnharmed is the no-destructive-default
// proof on real tmux: a live pane is refused PANE_BUSY (not retryable) and
// the sleep is still alive afterwards — nothing was killed.
func TestRespawnLivePaneAliveRefusedAndUnharmed(t *testing.T) {
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			e, srv, pane, start := liveRespawnEnsurer(t, b)
			res := e.RespawnPane(t.Context(), liveRespawnCmd(pane, start))
			if res.GetStatus() != typev1.Status_STATUS_FAILED ||
				res.GetReason() != typev1.ReasonCode_REASON_CODE_PANE_BUSY {
				t.Fatalf("RespawnPane = (%v, %v), want (FAILED, PANE_BUSY): %q",
					res.GetStatus(), res.GetReason(), res.GetMessage())
			}
			if res.GetRetryable() {
				t.Fatalf("live-pane refusal retryable = true, want false: %q", res.GetMessage())
			}
			dead := strings.TrimSpace(srv.Run(t, "display-message", "-p", "-t", pane, "-F", "#{pane_dead}"))
			if dead != "0" {
				t.Fatalf("the live pane died after a refused respawn (pane_dead = %q)", dead)
			}
		})
	}
}

// markerLines counts the marker's lines, or -1 when the pane has not
// written it yet.
func markerLines(t *testing.T, marker string) int {
	t.Helper()
	raw, err := os.ReadFile(marker) //nolint:gosec // G304: the path is the test's own t.TempDir file
	if err != nil {
		return -1
	}
	return len(strings.Split(strings.TrimSpace(string(raw)), "\n"))
}

// TestRespawnLiveDeadPaneRerunsItsCommand proves the respawn re-runs the
// pane's own command: a dead pane whose command appends one marker line
// per run gains exactly one more line after an OK respawn.
func TestRespawnLiveDeadPaneRerunsItsCommand(t *testing.T) {
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			e, srv, _, start := liveRespawnEnsurer(t, b)
			marker := filepath.Join(t.TempDir(), "marker")
			srv.Run(t, "new-window", "-d", "-t", "work", "exec sh -c 'echo run >> "+marker+"; exit 3'")
			pane := waitDead(t, srv, "work:1")
			if got := markerLines(t, marker); got != 1 {
				t.Fatalf("marker lines = %d, want the one run before the respawn", got)
			}
			res := e.RespawnPane(t.Context(), liveRespawnCmd(pane, start))
			if res.GetStatus() != typev1.Status_STATUS_OK {
				t.Fatalf("RespawnPane = (%v, %v), want OK: %q",
					res.GetStatus(), res.GetReason(), res.GetMessage())
			}
			deadline := time.Now().Add(10 * time.Second)
			for {
				if got := markerLines(t, marker); got == 2 {
					return
				}
				if time.Now().After(deadline) {
					t.Fatalf("marker lines = %d, want 2: the pane never re-ran", markerLines(t, marker))
				}
				time.Sleep(100 * time.Millisecond)
			}
		})
	}
}
