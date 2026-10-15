package control

import (
	"strings"
	"testing"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux/tmuxtest"
)

// liveInterruptEnsurer builds an Ensurer against a throwaway server running
// `exec sleep 60` and returns it with the server, the first pane's id and
// the server's start id. Every behavior below runs against real tmux. The
// sender stub stays empty: Interrupt sends its fixed C-c through the CLI,
// never through the observer client.
func liveInterruptEnsurer(t *testing.T, b tmuxtest.Binary) (*Ensurer, *tmuxtest.Server, string, string) {
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
	pane := strings.TrimSpace(srv.Run(t, "list-panes", "-t", "work", "-F", "#{pane_id}"))
	start, err := e.startID(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return e, srv, pane, start
}

func liveInterruptCmd(pane, start string) *agentv1.Interrupt {
	cmd := interruptCmd()
	cmd.Pane = &typev1.PaneKey{Server: "default", ServerStartId: start, PaneId: pane}
	return cmd
}

func TestInterruptLivePaneNotFound(t *testing.T) {
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			e, _, _, start := liveInterruptEnsurer(t, b)
			res := e.Interrupt(t.Context(), liveInterruptCmd("%99", start))
			if res.GetStatus() != typev1.Status_STATUS_FAILED ||
				res.GetReason() != typev1.ReasonCode_REASON_CODE_PANE_NOT_FOUND {
				t.Fatalf("Interrupt = (%v, %v), want (FAILED, PANE_NOT_FOUND): %q",
					res.GetStatus(), res.GetReason(), res.GetMessage())
			}
		})
	}
}

func TestInterruptLiveStaleStart(t *testing.T) {
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			e, _, pane, _ := liveInterruptEnsurer(t, b)
			res := e.Interrupt(t.Context(), liveInterruptCmd(pane, "0-0"))
			if res.GetStatus() != typev1.Status_STATUS_FAILED ||
				res.GetReason() != typev1.ReasonCode_REASON_CODE_PANE_NOT_FOUND {
				t.Fatalf("Interrupt = (%v, %v), want (FAILED, PANE_NOT_FOUND): %q",
					res.GetStatus(), res.GetReason(), res.GetMessage())
			}
		})
	}
}

func TestInterruptLiveDeadPane(t *testing.T) {
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			e, srv, _, start := liveInterruptEnsurer(t, b)
			srv.Run(t, "set-window-option", "-g", "remain-on-exit", "on")
			srv.Run(t, "new-window", "-d", "-t", "work", "exec sh -c 'exit 3'")
			pane := waitDead(t, srv, "work:1")
			res := e.Interrupt(t.Context(), liveInterruptCmd(pane, start))
			if res.GetStatus() != typev1.Status_STATUS_FAILED ||
				res.GetReason() != typev1.ReasonCode_REASON_CODE_PANE_BUSY {
				t.Fatalf("Interrupt = (%v, %v), want (FAILED, PANE_BUSY): %q",
					res.GetStatus(), res.GetReason(), res.GetMessage())
			}
			if res.GetRetryable() {
				t.Fatalf("dead-pane refusal retryable = true, want false: %q", res.GetMessage())
			}
		})
	}
}

// waitDead polls until the first pane of target reads dead, so the test
// never sleeps past the moment tmux reports it.
func waitDead(t *testing.T, srv *tmuxtest.Server, target string) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		pane := strings.TrimSpace(srv.Run(t, "list-panes", "-t", target, "-F", "#{pane_id}"))
		dead := strings.TrimSpace(srv.Run(t, "display-message", "-p", "-t", pane, "-F", "#{pane_dead}"))
		if dead == "1" {
			return pane
		}
		if time.Now().After(deadline) {
			t.Fatalf("the pane never read dead (pane_dead = %q)", dead)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestInterruptLiveSendsCtrlC(t *testing.T) {
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			e, srv, pane, start := liveInterruptEnsurer(t, b)
			srv.Run(t, "set-window-option", "-g", "remain-on-exit", "on")
			res := e.Interrupt(t.Context(), liveInterruptCmd(pane, start))
			if res.GetStatus() != typev1.Status_STATUS_OK {
				t.Fatalf("Interrupt = (%v, %v), want OK: %q",
					res.GetStatus(), res.GetReason(), res.GetMessage())
			}
			// The C-c landed only if the sleep died: a sent-but-lost
			// key leaves the pane alive.
			deadline := time.Now().Add(10 * time.Second)
			for {
				dead := strings.TrimSpace(srv.Run(t, "display-message", "-p", "-t", pane, "-F", "#{pane_dead}"))
				if dead == "1" {
					return
				}
				if time.Now().After(deadline) {
					t.Fatalf("the pane stayed alive after an OK Interrupt (pane_dead = %q)", dead)
				}
				time.Sleep(100 * time.Millisecond)
			}
		})
	}
}
