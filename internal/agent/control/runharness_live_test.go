package control

// F8 RunHarnessCommand against a real tmux server (tmuxtest): every refusal
// below proves the gate on live pane reads, and the last test proves the
// production seam, a real observer client typing PurposeHarnessCommand
// lines into a live pane.

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux/tmuxtest"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux/typing"
	"github.com/linuxdynasty/fleet-board/internal/proc"
)

// liveHarnessEnsurer builds an Ensurer against a throwaway server running
// `exec sleep 60` and returns it with the server, the first pane's id and
// the server's start id. The sender and input check are stubs, so a
// refusal proves the gate, not the rig, typed nothing.
func liveHarnessEnsurer(t *testing.T, b tmuxtest.Binary, sender Sender, check CheckInputFunc) (*Ensurer, *tmuxtest.Server, string, string) {
	t.Helper()
	srv := tmuxtest.Start(t, b, "-s", "work", "-x", "120", "-y", "20", "exec sleep 60")
	o := testOptions()
	o.Socket = srv.Socket
	if b.Path != "" {
		o.Tmux = b.Path
	}
	o.Sender = sender
	o.CheckInput = check
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

func liveHarnessCmd(pane, start string) *agentv1.RunHarnessCommand {
	cmd := runHarnessCmd()
	cmd.Pane = &typev1.PaneKey{Server: "default", ServerStartId: start, PaneId: pane}
	return cmd
}

func TestRunHarnessLivePaneNotFound(t *testing.T) {
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			stub := &stubSender{}
			e, _, _, start := liveHarnessEnsurer(t, b, stub, stubCheckInput)
			res := e.RunHarnessCommand(t.Context(), liveHarnessCmd("%99", start))
			if res.GetStatus() != typev1.Status_STATUS_FAILED ||
				res.GetReason() != typev1.ReasonCode_REASON_CODE_PANE_NOT_FOUND {
				t.Fatalf("RunHarnessCommand = (%v, %v), want (FAILED, PANE_NOT_FOUND): %q",
					res.GetStatus(), res.GetReason(), res.GetMessage())
			}
			if len(stub.sent) != 0 {
				t.Fatalf("a refused RunHarnessCommand typed %d lines", len(stub.sent))
			}
		})
	}
}

func TestRunHarnessLiveStaleStart(t *testing.T) {
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			stub := &stubSender{}
			e, _, pane, _ := liveHarnessEnsurer(t, b, stub, stubCheckInput)
			res := e.RunHarnessCommand(t.Context(), liveHarnessCmd(pane, "0-0"))
			if res.GetStatus() != typev1.Status_STATUS_FAILED ||
				res.GetReason() != typev1.ReasonCode_REASON_CODE_PANE_NOT_FOUND {
				t.Fatalf("RunHarnessCommand = (%v, %v), want (FAILED, PANE_NOT_FOUND): %q",
					res.GetStatus(), res.GetReason(), res.GetMessage())
			}
			if len(stub.sent) != 0 {
				t.Fatalf("a refused RunHarnessCommand typed %d lines", len(stub.sent))
			}
		})
	}
}

func TestRunHarnessLiveShellRefusedAndScreenUnchanged(t *testing.T) {
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			stub := &stubSender{}
			e, srv, pane, start := liveHarnessEnsurer(t, b, stub, stubCheckInput)
			before := captureNow(t, srv, pane)
			res := e.RunHarnessCommand(t.Context(), liveHarnessCmd(pane, start))
			if res.GetStatus() != typev1.Status_STATUS_FAILED ||
				res.GetReason() != typev1.ReasonCode_REASON_CODE_PANE_BUSY {
				t.Fatalf("RunHarnessCommand = (%v, %v), want (FAILED, PANE_BUSY): %q",
					res.GetStatus(), res.GetReason(), res.GetMessage())
			}
			if !res.GetRetryable() {
				t.Fatalf("shell refusal retryable = false, want true: %q", res.GetMessage())
			}
			if len(stub.sent) != 0 {
				t.Fatalf("a refused RunHarnessCommand typed %d lines", len(stub.sent))
			}
			if after := captureNow(t, srv, pane); after != before {
				t.Fatalf("the pane screen changed on a refusal:\nbefore %q\nafter %q", before, after)
			}
		})
	}
}

func TestRunHarnessLiveDeadPane(t *testing.T) {
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			stub := &stubSender{}
			e, srv, _, start := liveHarnessEnsurer(t, b, stub, stubCheckInput)
			srv.Run(t, "set-window-option", "-g", "remain-on-exit", "on")
			srv.Run(t, "new-window", "-d", "-t", "work", "exec sh -c 'exit 3'")
			var pane string
			deadline := time.Now().Add(10 * time.Second)
			for {
				pane = strings.TrimSpace(srv.Run(t, "list-panes", "-t", "work:1", "-F", "#{pane_id}"))
				dead := strings.TrimSpace(srv.Run(t, "display-message", "-p", "-t", pane, "-F", "#{pane_dead}"))
				if dead == "1" {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("the exit-3 pane never read dead (pane_dead = %q)", dead)
				}
				time.Sleep(100 * time.Millisecond)
			}
			res := e.RunHarnessCommand(t.Context(), liveHarnessCmd(pane, start))
			if res.GetStatus() != typev1.Status_STATUS_FAILED ||
				res.GetReason() != typev1.ReasonCode_REASON_CODE_PANE_BUSY {
				t.Fatalf("RunHarnessCommand = (%v, %v), want (FAILED, PANE_BUSY): %q",
					res.GetStatus(), res.GetReason(), res.GetMessage())
			}
			if len(stub.sent) != 0 {
				t.Fatalf("a refused RunHarnessCommand typed %d lines", len(stub.sent))
			}
		})
	}
}

func TestRunHarnessLiveRecentInput(t *testing.T) {
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			now := time.Now()
			stub := &stubSender{}
			check := func(context.Context, string) (time.Time, error) { return now.Add(-time.Minute), nil }
			e, _, pane, start := liveHarnessEnsurer(t, b, stub, check)
			// The input check runs before the harness read, like F3, so a
			// recent key wins even on this shell pane.
			res := e.RunHarnessCommand(t.Context(), liveHarnessCmd(pane, start))
			if res.GetStatus() != typev1.Status_STATUS_FAILED ||
				res.GetReason() != typev1.ReasonCode_REASON_CODE_PANE_BUSY ||
				!strings.Contains(res.GetMessage(), "a person typed") {
				t.Fatalf("RunHarnessCommand = (%v, %v), want (FAILED, PANE_BUSY) for recent input: %q",
					res.GetStatus(), res.GetReason(), res.GetMessage())
			}
			if !res.GetRetryable() {
				t.Fatalf("input refusal retryable = false, want true: %q", res.GetMessage())
			}
			if len(stub.sent) != 0 {
				t.Fatalf("a refused RunHarnessCommand typed %d lines", len(stub.sent))
			}
		})
	}
}

// TestRunHarnessLiveSenderTypesThroughARealClient proves the production
// seam: a real observer client behind the Sender interface types
// PurposeHarnessCommand lines into real tmux and reads the echo back. The
// gate itself never sees this pane (cat is no harness); the Sends below
// stand in for lines a passed gate built.
func TestRunHarnessLiveSenderTypesThroughARealClient(t *testing.T) {
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			srv := tmuxtest.Start(t, b, "-s", "work", "-x", "120", "-y", "20", "exec cat")
			srv.Run(t, "new-window", "-d", "-t", "work", "exec cat")
			pane := strings.TrimSpace(srv.Run(t, "list-panes", "-t", "work:1", "-F", "#{pane_id}"))
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
			var sender Sender = c
			start := c.ServerStartID()
			key := &typev1.PaneKey{Server: "default", ServerStartId: start, PaneId: pane}
			text := "/status"
			lit, err := typing.NewLiteral(nil, key, typing.PurposeHarnessCommand, text)
			if err != nil {
				t.Fatal(err)
			}
			enter, err := typing.NewKeys(nil, key, typing.PurposeHarnessCommand, typing.KeyEnter)
			if err != nil {
				t.Fatal(err)
			}
			if err := sender.Send(ctx, lit); err != nil {
				t.Fatal(err)
			}
			if err := sender.Send(ctx, enter); err != nil {
				t.Fatal(err)
			}
			// cat echoes the line after Enter: the screen must hold it.
			n, err := strconv.Atoi(strings.TrimPrefix(pane, "%"))
			if err != nil {
				t.Fatal(err)
			}
			id := tmux.PaneID(n)
			found := false
			deadline := time.Now().Add(10 * time.Second)
			for !found && time.Now().Before(deadline) {
				got, err := sender.CaptureStyled(ctx, id)
				if err != nil {
					t.Fatal(err)
				}
				for _, l := range got.Lines {
					if l == text {
						found = true
					}
				}
				if !found {
					time.Sleep(100 * time.Millisecond)
				}
			}
			if !found {
				t.Fatalf("the typed command never echoed in the pane")
			}
			cancel()
			<-done
		})
	}
}
