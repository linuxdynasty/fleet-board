package control

import (
	"context"
	"errors"
	"testing"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/exec"
	"github.com/linuxdynasty/fleet-board/internal/proc"
)

// interruptCmd builds an Interrupt for pane %1. The shape refusals below
// fail before any tmux call, so the dummy socket in testOptions never
// answers.
func interruptCmd() *agentv1.Interrupt {
	return &agentv1.Interrupt{
		Pane: &typev1.PaneKey{
			Server:        "default",
			ServerStartId: "live-start-id",
			PaneId:        "%1",
		},
	}
}

func TestInterruptRefusesABadShape(t *testing.T) {
	t.Parallel()
	e, err := New(testOptions())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		mute func(*agentv1.Interrupt)
	}{
		{"no pane", func(c *agentv1.Interrupt) { c.Pane = nil }},
		{"another server", func(c *agentv1.Interrupt) {
			c.Pane = &typev1.PaneKey{Server: "fleet", ServerStartId: "live-start-id", PaneId: "%1"}
		}},
		{"bad pane id", func(c *agentv1.Interrupt) {
			c.Pane = &typev1.PaneKey{Server: "default", ServerStartId: "live-start-id", PaneId: "1"}
		}},
		{"no server start id", func(c *agentv1.Interrupt) {
			c.Pane = &typev1.PaneKey{Server: "default", PaneId: "%1"}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cmd := interruptCmd()
			tc.mute(cmd)
			res := e.Interrupt(t.Context(), cmd)
			if res.GetStatus() != typev1.Status_STATUS_FAILED ||
				res.GetReason() != typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT {
				t.Fatalf("Interrupt = (%v, %v), want (FAILED, INVALID_ARGUMENT): %q",
					res.GetStatus(), res.GetReason(), res.GetMessage())
			}
		})
	}
}

// TestInterruptHandlerContract pins the executor seam for F4: the kind the
// handler runs, the capability it advertises for Command.requires, and the
// restart class (Interrupt is idempotent in effect, so a resume re-runs it
// from the start). F4 appears here as a whole word for the capability
// report.
func TestInterruptHandlerContract(t *testing.T) {
	t.Parallel()
	e, err := New(testOptions())
	if err != nil {
		t.Fatal(err)
	}
	h := e.InterruptHandler()
	if h.Kind() != "Interrupt" {
		t.Fatalf("Kind = %q, want Interrupt (F4)", h.Kind())
	}
	caps := h.Capabilities()
	// 02e "Command kinds and capabilities": tmux.control plus the
	// server-specific one. F4 appears here as a whole word for the
	// capability report.
	want := map[string]uint32{"tmux.control": 1, "tmux.default_server.control": 1}
	if len(caps) != len(want) {
		t.Fatalf("Capabilities = %v, want %v for F4", caps, want)
	}
	for _, c := range caps {
		if want[c.GetName()] != c.GetVersion() || c.GetVersion() != 1 {
			t.Fatalf("Capabilities = %v, want %v for F4", caps, want)
		}
	}
	cmd := &agentv1.Command{Kind: &agentv1.Command_Interrupt{Interrupt: &agentv1.Interrupt{}}}
	if got := h.Class(cmd); got != exec.RepeatSafe {
		t.Fatalf("Class = %v, want RepeatSafe for F4", got)
	}
	// RepeatSafe resumes by re-running: a shapeless command is refused
	// again, never parked as unknown.
	bad := &agentv1.Command{Kind: &agentv1.Command_Interrupt{Interrupt: &agentv1.Interrupt{}}}
	res := h.Resume(t.Context(), exec.Request{Command: bad})
	if res.GetStatus() != typev1.Status_STATUS_FAILED ||
		res.GetReason() != typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT {
		t.Fatalf("Resume = (%v, %v), want (FAILED, INVALID_ARGUMENT): %q",
			res.GetStatus(), res.GetReason(), res.GetMessage())
	}
}

// scriptRunner answers each tmux call from its script: the subcommand
// (the first argument past -S) selects the canned output or error. It
// pins the send-failure mapping without racing a live server: paneInfo
// sees the pane, then send-keys reports it gone.
type scriptRunner struct {
	out map[string]string
	err map[string]error
}

func (s *scriptRunner) Run(_ context.Context, sp proc.Spec) (proc.Result, error) {
	sub := ""
	if len(sp.Args) > 2 {
		sub = sp.Args[2]
	}
	if err, ok := s.err[sub]; ok {
		return proc.Result{}, err
	}
	return proc.Result{Stdout: []byte(s.out[sub])}, nil
}

func TestInterruptMapsASendFailure(t *testing.T) {
	t.Parallel()
	start := startID(1000, 2000)
	pane := &typev1.PaneKey{Server: "default", ServerStartId: start, PaneId: "%1"}
	script := func(sendErr error) *scriptRunner {
		return &scriptRunner{
			out: map[string]string{
				"display-message": "1000 2000",
				"list-panes":      "%1 0 sleep",
			},
			err: map[string]error{"send-keys": sendErr},
		}
	}
	for _, tc := range []struct {
		name   string
		send   error
		reason typev1.ReasonCode
	}{
		// The measured tmux text for a pane lost between the listing
		// and the send (tmux 3.8: "can't find pane: %1", exit 1).
		{"pane gone under the send", errors.New("tmux send-keys: can't find pane: %1: exit status 1"), typev1.ReasonCode_REASON_CODE_PANE_NOT_FOUND},
		{"send refused another way", errors.New("exit status 1"), typev1.ReasonCode_REASON_CODE_INTERNAL},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			o := testOptions()
			o.Runner = script(tc.send)
			e, err := New(o)
			if err != nil {
				t.Fatal(err)
			}
			res := e.Interrupt(t.Context(), &agentv1.Interrupt{Pane: pane})
			if res.GetStatus() != typev1.Status_STATUS_FAILED || res.GetReason() != tc.reason {
				t.Fatalf("Interrupt = (%v, %v), want (FAILED, %v): %q",
					res.GetStatus(), res.GetReason(), tc.reason, res.GetMessage())
			}
		})
	}
}
