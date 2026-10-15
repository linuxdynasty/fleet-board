package control

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/exec"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux"
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

// TestInterruptSendsThroughTheObserverClient pins the routing the review
// thread requires: the fixed C-c travels as a typing.Line through the
// observer client — never a one-shot CLI send-keys — carrying the pane
// key's server and start id, so tmux.Client.Send refuses a stale
// generation before writing anything. The scripted runner fails any
// send-keys subcommand, so on the old path the result below is not OK.
func TestInterruptSendsThroughTheObserverClient(t *testing.T) {
	t.Parallel()
	start := startID(1000, 2000)
	o := testOptions()
	o.Runner = &scriptRunner{
		out: map[string]string{
			"display-message": "1000 2000",
			"list-panes":      "%1 0 sleep",
		},
		err: map[string]error{"send-keys": errors.New("must not send through one-shot CLI")},
	}
	stub := &stubSender{}
	o.Sender = stub
	e, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	res := e.Interrupt(t.Context(), &agentv1.Interrupt{Pane: &typev1.PaneKey{
		Server: "default", ServerStartId: start, PaneId: "%1",
	}})
	if res.GetStatus() != typev1.Status_STATUS_OK {
		t.Fatalf("Interrupt = (%v, %v), want OK: %q",
			res.GetStatus(), res.GetReason(), res.GetMessage())
	}
	if len(stub.sent) != 1 {
		t.Fatalf("sender got %d lines, want the one C-c line", len(stub.sent))
	}
	l := stub.sent[0]
	if l.IsZero() || l.Server() != "default" || l.ServerStartID() != start || l.PaneID() != "%1" {
		t.Fatalf("line targets %s/%s/%s, want default/%s/%%1",
			l.Server(), l.ServerStartID(), l.PaneID(), start)
	}
	if cmd, err := l.Command(); err != nil || cmd != "send-keys -t %1 C-c" {
		t.Fatalf("Command() = %q, %v; want %q", cmd, err, "send-keys -t %1 C-c")
	}
}

// vanishRunner answers the first list-panes with the pane alive and every
// later one without it: the pane vanishes between the listing check and
// the send's re-read.
type vanishRunner struct {
	calls atomic.Int64
}

func (r *vanishRunner) Run(_ context.Context, sp proc.Spec) (proc.Result, error) {
	sub := ""
	if len(sp.Args) > 2 {
		sub = sp.Args[2]
	}
	switch sub {
	case "display-message":
		return proc.Result{Stdout: []byte("1000 2000")}, nil
	case "list-panes":
		if r.calls.Add(1) == 1 {
			return proc.Result{Stdout: []byte("%1 0 sleep")}, nil
		}
		return proc.Result{}, nil
	default:
		return proc.Result{}, errors.New("unexpected tmux call " + sub)
	}
}

// TestInterruptMapsSenderRefusals pins the send-failure mapping on the new
// path: the observer client's generation, attach and reply errors read the
// same reasons the one-shot CLI mapping read. A refused send wrote
// nothing, so a %error re-reads the listing to say whether the pane went
// away under the send (PANE_NOT_FOUND) or tmux refused another way
// (INTERNAL).
func TestInterruptMapsSenderRefusals(t *testing.T) {
	t.Parallel()
	start := startID(1000, 2000)
	pane := &typev1.PaneKey{Server: "default", ServerStartId: start, PaneId: "%1"}
	script := func() *scriptRunner {
		return &scriptRunner{
			out: map[string]string{
				"display-message": "1000 2000",
				"list-panes":      "%1 0 sleep",
			},
		}
	}
	for _, tc := range []struct {
		name   string
		send   error
		status typev1.Status
		reason typev1.ReasonCode
	}{
		{"stale server start", tmux.ErrWrongServer, typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_PANE_NOT_FOUND},
		{"client detached", tmux.ErrNotAttached, typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_TMUX_SERVER_DOWN},
		{"reply lost", tmux.ErrMaybeApplied, typev1.Status_STATUS_UNKNOWN_OUTCOME, typev1.ReasonCode_REASON_CODE_UNSPECIFIED},
		{"reply lost on a dead attach", errors.Join(tmux.ErrNotAttached, tmux.ErrMaybeApplied), typev1.Status_STATUS_UNKNOWN_OUTCOME, typev1.ReasonCode_REASON_CODE_UNSPECIFIED},
		{"send refused with the pane still listed", tmux.ErrCommandFailed, typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INTERNAL},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			o := testOptions()
			o.Runner = script()
			o.Sender = &stubSender{failAt: 1, failErr: tc.send}
			e, err := New(o)
			if err != nil {
				t.Fatal(err)
			}
			res := e.Interrupt(t.Context(), &agentv1.Interrupt{Pane: pane})
			if res.GetStatus() != tc.status || res.GetReason() != tc.reason {
				t.Fatalf("Interrupt = (%v, %v), want (%v, %v): %q",
					res.GetStatus(), res.GetReason(), tc.status, tc.reason, res.GetMessage())
			}
		})
	}
	t.Run("pane gone under the send", func(t *testing.T) {
		t.Parallel()
		o := testOptions()
		o.Runner = &vanishRunner{}
		o.Sender = &stubSender{failAt: 1, failErr: tmux.ErrCommandFailed}
		e, err := New(o)
		if err != nil {
			t.Fatal(err)
		}
		res := e.Interrupt(t.Context(), &agentv1.Interrupt{Pane: pane})
		if res.GetStatus() != typev1.Status_STATUS_FAILED ||
			res.GetReason() != typev1.ReasonCode_REASON_CODE_PANE_NOT_FOUND {
			t.Fatalf("Interrupt = (%v, %v), want (FAILED, PANE_NOT_FOUND): %q",
				res.GetStatus(), res.GetReason(), res.GetMessage())
		}
	})
}
