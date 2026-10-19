package control

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux/tmuxtest"
	"github.com/linuxdynasty/fleet-board/internal/proc"
)

// liveInterruptEnsurer builds an Ensurer against a throwaway server running
// `exec sleep 60` and returns it with the server, the first pane's id and
// the server's start id. Every behavior below runs against real tmux. The
// sender is a real observer client attached to the server: Interrupt sends
// its fixed C-c as a typing.Line through it, so the server-start check is
// atomic with the write.
func liveInterruptEnsurer(t *testing.T, b tmuxtest.Binary) (*Ensurer, *tmuxtest.Server, string, string) {
	t.Helper()
	srv := tmuxtest.Start(t, b, "-s", "work", "-x", "120", "-y", "20", "exec sleep 60")
	runner, err := proc.NewRunner(nil)
	if err != nil {
		t.Fatal(err)
	}
	c, err := tmux.NewClient(tmux.ClientOptions{
		Program: b.Path, Socket: srv.Socket, Server: "default", Runner: runner,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	if err := c.WaitState(ctx, tmux.StateUp); err != nil {
		t.Fatal(err)
	}
	o := testOptions()
	o.Socket = srv.Socket
	if b.Path != "" {
		o.Tmux = b.Path
	}
	o.Sender = c
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

// Missing pane IDs must not leave behind locks in the long-lived ensurer.
func TestInterruptLiveMissingPanesReleaseLocks(t *testing.T) {
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			live, _, _, start := liveInterruptEnsurer(t, b)
			e, err := NewInterrupt(Options{
				Runner: live.runner, Caller: live.caller, Tmux: live.tmux,
				Socket: live.socket, Server: live.server, Kind: live.kind,
				Sender: live.sender, Rec: live.rec,
			})
			if err != nil {
				t.Fatal(err)
			}
			for id := 90; id < 98; id++ {
				res := e.Interrupt(t.Context(), liveInterruptCmd(fmt.Sprintf("%%%d", id), start))
				if res.GetStatus() != typev1.Status_STATUS_FAILED ||
					res.GetReason() != typev1.ReasonCode_REASON_CODE_PANE_NOT_FOUND {
					t.Fatalf("Interrupt missing pane %d = (%v, %v), want (FAILED, PANE_NOT_FOUND): %q",
						id, res.GetStatus(), res.GetReason(), res.GetMessage())
				}
			}
			e.paneMu.Lock()
			retained := len(e.panes)
			e.paneMu.Unlock()
			if retained != 0 {
				t.Fatalf("%d locks retained after refused missing pane IDs, want none", retained)
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

// restartRunner replays the interleaving the review thread names: the
// start-id and listing checks saw the old server (generation A, frozen
// answers) while the send faces the new server on the same socket
// (generation B, live). Every subcommand but send-keys answers from the
// frozen generation; send-keys runs for real against the live socket, and
// sends counts those calls. A send that bypasses the observer client's
// server-start check lands on the restarted server's reused pane.
type restartRunner struct {
	frozen map[string]string
	srv    *tmuxtest.Server
	sends  atomic.Int64
}

func (r *restartRunner) Run(_ context.Context, sp proc.Spec) (proc.Result, error) {
	sub := ""
	if len(sp.Args) > 2 {
		sub = sp.Args[2]
	}
	if sub == "send-keys" {
		r.sends.Add(1)
		out, err := r.srv.Try(sp.Args[2:]...)
		return proc.Result{Stdout: []byte(out)}, err
	}
	return proc.Result{Stdout: []byte(r.frozen[sub])}, nil
}

// TestInterruptLiveRestartKeepsCcOffTheReusedPane is the regression test
// for the review thread on interrupt.go: tmux pane ids restart from zero
// with a new server, so a C-c sent through a fresh one-shot CLI call after
// the checks reconnects on the same socket and lands on the new server's
// reused %N pane. The interrupt must travel through the observer client,
// which refuses a line built for an older server start before writing
// anything (tmux.Client.Send). The fixture is a real restart on one
// throwaway socket: only the two check answers are scripted, everything
// the send can reach is live.
func TestInterruptLiveRestartKeepsCcOffTheReusedPane(t *testing.T) {
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			srv := tmuxtest.Start(t, b, "-s", "work", "-x", "120", "-y", "20", "exec sleep 60")
			paneA := strings.TrimSpace(srv.Run(t, "list-panes", "-t", "work", "-F", "#{pane_id}"))
			srv.Kill(t)
			srv.NewSession(t, "-s", "work", "-x", "120", "-y", "20", "exec sleep 60")
			paneB := strings.TrimSpace(srv.Run(t, "list-panes", "-t", "work", "-F", "#{pane_id}"))
			if paneB != paneA {
				t.Fatalf("fixture: the restarted server reused no pane id (was %s, now %s)", paneA, paneB)
			}
			srv.Run(t, "set-window-option", "-g", "remain-on-exit", "on")
			runner, err := proc.NewRunner(nil)
			if err != nil {
				t.Fatal(err)
			}
			c, err := tmux.NewClient(tmux.ClientOptions{
				Program: b.Path, Socket: srv.Socket, Server: "default", Runner: runner,
			})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- c.Run(ctx) }()
			if err := c.WaitState(ctx, tmux.StateUp); err != nil {
				t.Fatal(err)
			}
			// The frozen checks answer for a generation that is gone:
			// "1000 2000" reads a start id no live server has.
			frozen := startID(1000, 2000)
			if frozen == c.ServerStartID() {
				t.Fatalf("fixture: the frozen start id %q matches the live server", frozen)
			}
			rr := &restartRunner{
				frozen: map[string]string{
					"display-message": "1000 2000",
					"list-panes":      paneB + " 0 sleep",
				},
				srv: srv,
			}
			o := testOptions()
			o.Socket = srv.Socket
			if b.Path != "" {
				o.Tmux = b.Path
			}
			o.Runner = rr
			o.Sender = c
			e, err := New(o)
			if err != nil {
				t.Fatal(err)
			}
			res := e.Interrupt(ctx, &agentv1.Interrupt{Pane: &typev1.PaneKey{
				Server: "default", ServerStartId: frozen, PaneId: paneB,
			}})
			// A misdelivered C-c kills the sleep within a moment; the
			// new generation's pane must stay alive.
			deadline := time.Now().Add(5 * time.Second)
			for {
				dead := strings.TrimSpace(srv.Run(t, "display-message", "-p", "-t", paneB, "-F", "#{pane_dead}"))
				if dead == "1" {
					t.Fatalf("C-c reached the restarted server's reused pane %s", paneB)
				}
				if time.Now().After(deadline) {
					break
				}
				time.Sleep(100 * time.Millisecond)
			}
			if n := rr.sends.Load(); n != 0 {
				t.Fatalf("interrupt sent %d one-shot CLI send-keys past the checks, want none", n)
			}
			if res.GetStatus() != typev1.Status_STATUS_FAILED ||
				res.GetReason() != typev1.ReasonCode_REASON_CODE_PANE_NOT_FOUND {
				t.Fatalf("Interrupt = (%v, %v), want (FAILED, PANE_NOT_FOUND): %q",
					res.GetStatus(), res.GetReason(), res.GetMessage())
			}
			cancel()
			<-done
		})
	}
}
