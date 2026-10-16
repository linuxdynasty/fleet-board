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

// errStubRespawn fails the respawn-pane subcommand on scripted runners that
// must never reach it (a live pane is refused before any respawn call).
var errStubRespawn = errors.New("stub tmux refused respawn-pane")

// respawnCmd builds a RespawnPane for pane %1. The shape refusals below
// fail before any tmux call, so the dummy socket in testOptions never
// answers.
func respawnCmd() *agentv1.RespawnPane {
	return &agentv1.RespawnPane{
		Pane: &typev1.PaneKey{
			Server:        "default",
			ServerStartId: "live-start-id",
			PaneId:        "%1",
		},
	}
}

func TestRespawnRefusesABadShape(t *testing.T) {
	t.Parallel()
	e, err := New(testOptions())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		mute func(*agentv1.RespawnPane)
	}{
		{"no pane", func(c *agentv1.RespawnPane) { c.Pane = nil }},
		{"another server", func(c *agentv1.RespawnPane) {
			c.Pane = &typev1.PaneKey{Server: "fleet", ServerStartId: "live-start-id", PaneId: "%1"}
		}},
		{"bad pane id", func(c *agentv1.RespawnPane) {
			c.Pane = &typev1.PaneKey{Server: "default", ServerStartId: "live-start-id", PaneId: "1"}
		}},
		{"no server start id", func(c *agentv1.RespawnPane) {
			c.Pane = &typev1.PaneKey{Server: "default", PaneId: "%1"}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cmd := respawnCmd()
			tc.mute(cmd)
			res := e.RespawnPane(t.Context(), cmd)
			if res.GetStatus() != typev1.Status_STATUS_FAILED ||
				res.GetReason() != typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT {
				t.Fatalf("RespawnPane = (%v, %v), want (FAILED, INVALID_ARGUMENT): %q",
					res.GetStatus(), res.GetReason(), res.GetMessage())
			}
		})
	}
}

// TestRespawnRefusesHarnessFields pins the F1/F2 rule for F5: this slice
// respawns the pane's own command, so any harness restart field waits for
// the adapter slice. A profile or conversation without a harness is the
// caller's malformation, like F2's.
func TestRespawnRefusesHarnessFields(t *testing.T) {
	t.Parallel()
	e, err := New(testOptions())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		change func(*agentv1.RespawnPane)
		status typev1.Status
		reason typev1.ReasonCode
	}{
		{"harness waits for its slice", func(c *agentv1.RespawnPane) {
			c.Harness = &typev1.Harness{Name: "claude"}
		}, typev1.Status_STATUS_UNSUPPORTED, typev1.ReasonCode_REASON_CODE_HARNESS_NOT_ALLOWED},
		{"resume waits for its slice", func(c *agentv1.RespawnPane) {
			c.Resume = true
		}, typev1.Status_STATUS_UNSUPPORTED, typev1.ReasonCode_REASON_CODE_HARNESS_NOT_ALLOWED},
		{"profile without a harness", func(c *agentv1.RespawnPane) {
			c.Profile = "work"
		}, typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT},
		{"conversation without a harness", func(c *agentv1.RespawnPane) {
			c.HarnessSessionId = "sess-1"
		}, typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cmd := respawnCmd()
			tc.change(cmd)
			res := e.RespawnPane(t.Context(), cmd)
			if res.GetStatus() != tc.status || res.GetReason() != tc.reason {
				t.Fatalf("RespawnPane = (%v, %v), want (%v, %v): %q",
					res.GetStatus(), res.GetReason(), tc.status, tc.reason, res.GetMessage())
			}
		})
	}
}

// TestRespawnHandlerContract pins the executor seam for F5: the kind the
// handler runs, the capability it advertises for Command.requires, and the
// restart class (02e: RespawnPane is repeat safe, so a resume re-runs it
// from the start). F5 appears here as a whole word for the capability
// report.
func TestRespawnHandlerContract(t *testing.T) {
	t.Parallel()
	e, err := New(testOptions())
	if err != nil {
		t.Fatal(err)
	}
	h := e.RespawnHandler()
	if h.Kind() != "RespawnPane" {
		t.Fatalf("Kind = %q, want RespawnPane (F5)", h.Kind())
	}
	caps := h.Capabilities()
	// 02e "Command kinds and capabilities": tmux.control plus the
	// server-specific one. F5 appears here as a whole word for the
	// capability report.
	want := map[string]uint32{"tmux.control": 1, "tmux.default_server.control": 1}
	if len(caps) != len(want) {
		t.Fatalf("Capabilities = %v, want %v for F5", caps, want)
	}
	for _, c := range caps {
		if want[c.GetName()] != c.GetVersion() || c.GetVersion() != 1 {
			t.Fatalf("Capabilities = %v, want %v for F5", caps, want)
		}
	}
	cmd := &agentv1.Command{Kind: &agentv1.Command_RespawnPane{RespawnPane: &agentv1.RespawnPane{}}}
	if got := h.Class(cmd); got != exec.RepeatSafe {
		t.Fatalf("Class = %v, want RepeatSafe for F5", got)
	}
	// RepeatSafe resumes by re-running: a shapeless command is refused
	// again, never parked as unknown.
	bad := &agentv1.Command{Kind: &agentv1.Command_RespawnPane{RespawnPane: &agentv1.RespawnPane{}}}
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

// TestRespawnPaneChecks pins the F4-modeled listing checks on a scripted
// runner: a stale start id and a missing pane read PANE_NOT_FOUND before
// any respawn is attempted.
func TestRespawnPaneChecks(t *testing.T) {
	t.Parallel()
	start := startID(1000, 2000)
	pane := &typev1.PaneKey{Server: "default", ServerStartId: start, PaneId: "%1"}
	script := func(listing string) *scriptRunner {
		return &scriptRunner{
			out: map[string]string{
				"display-message": "1000 2000",
				"list-panes":      listing,
			},
		}
	}
	for _, tc := range []struct {
		name    string
		key     *typev1.PaneKey
		listing string
		reason  typev1.ReasonCode
	}{
		{"stale server start", &typev1.PaneKey{Server: "default", ServerStartId: "0-0", PaneId: "%1"}, "%1 0 sleep", typev1.ReasonCode_REASON_CODE_PANE_NOT_FOUND},
		{"missing pane", pane, "%2 0 sleep", typev1.ReasonCode_REASON_CODE_PANE_NOT_FOUND},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			o := testOptions()
			o.Runner = script(tc.listing)
			e, err := New(o)
			if err != nil {
				t.Fatal(err)
			}
			res := e.RespawnPane(t.Context(), &agentv1.RespawnPane{Pane: tc.key})
			if res.GetStatus() != typev1.Status_STATUS_FAILED || res.GetReason() != tc.reason {
				t.Fatalf("RespawnPane = (%v, %v), want (FAILED, %v): %q",
					res.GetStatus(), res.GetReason(), tc.reason, res.GetMessage())
			}
		})
	}
}

// countRunner delegates to an inner Runner and counts respawn-pane calls,
// so the live-pane test proves no respawn was attempted, not just that a
// refused one maps back to PANE_BUSY.
type countRunner struct {
	inner Runner
	calls int
}

func (c *countRunner) Run(ctx context.Context, sp proc.Spec) (proc.Result, error) {
	if len(sp.Args) > 2 && sp.Args[2] == "respawn-pane" {
		c.calls++
	}
	return c.inner.Run(ctx, sp)
}

// TestRespawnNeverTouchesALivePane is the no-destructive-default proof: a
// live pane is refused PANE_BUSY before any respawn call. The counting
// runner proves no respawn-pane call was attempted (a refused call would
// still map back to PANE_BUSY on the re-read). The refusal is not
// retryable: the hub re-evaluates instead of retrying a kill into the
// deadline.
func TestRespawnNeverTouchesALivePane(t *testing.T) {
	t.Parallel()
	start := startID(1000, 2000)
	o := testOptions()
	cr := &countRunner{inner: &scriptRunner{
		out: map[string]string{
			"display-message": "1000 2000",
			"list-panes":      "%1 0 sleep",
		},
		err: map[string]error{"respawn-pane": errStubRespawn},
	}}
	o.Runner = cr
	stub := &stubSender{}
	o.Sender = stub
	e, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	res := e.RespawnPane(t.Context(), &agentv1.RespawnPane{Pane: &typev1.PaneKey{
		Server: "default", ServerStartId: start, PaneId: "%1",
	}})
	if res.GetStatus() != typev1.Status_STATUS_FAILED ||
		res.GetReason() != typev1.ReasonCode_REASON_CODE_PANE_BUSY {
		t.Fatalf("RespawnPane = (%v, %v), want (FAILED, PANE_BUSY): %q",
			res.GetStatus(), res.GetReason(), res.GetMessage())
	}
	if res.GetRetryable() {
		t.Fatalf("live-pane refusal retryable = true, want false: %q", res.GetMessage())
	}
	if cr.calls != 0 {
		t.Fatalf("respawn-pane attempted %d times on a live pane, want none", cr.calls)
	}
	if len(stub.sent) != 0 {
		t.Fatalf("sender got %d lines, want none: a respawn types nothing", len(stub.sent))
	}
}

// TestRespawnDeadPaneRespawns pins the happy path on a scripted runner: a
// dead pane is respawned through a plain one-shot respawn-pane call — the
// pane's own command re-runs, with no -k flag that could kill live work.
func TestRespawnDeadPaneRespawns(t *testing.T) {
	t.Parallel()
	start := startID(1000, 2000)
	o := testOptions()
	o.Runner = &scriptRunner{
		out: map[string]string{
			"display-message": "1000 2000",
			"list-panes":      "%1 1 sh",
			"respawn-pane":    "",
		},
	}
	e, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	res := e.RespawnPane(t.Context(), &agentv1.RespawnPane{Pane: &typev1.PaneKey{
		Server: "default", ServerStartId: start, PaneId: "%1",
	}})
	if res.GetStatus() != typev1.Status_STATUS_OK {
		t.Fatalf("RespawnPane = (%v, %v), want OK: %q",
			res.GetStatus(), res.GetReason(), res.GetMessage())
	}
}

// seqRunner answers each tmux call from a per-subcommand queue, so the
// respawn-failure mapping is pinned without racing a live server: the
// first list-panes sees the pane dead, the respawn-pane call fails, and
// the re-read sees what the failure left behind.
type seqRunner struct {
	out map[string][]string
	err map[string]error
}

func (s *seqRunner) Run(_ context.Context, sp proc.Spec) (proc.Result, error) {
	sub := ""
	if len(sp.Args) > 2 {
		sub = sp.Args[2]
	}
	if err, ok := s.err[sub]; ok {
		return proc.Result{}, err
	}
	q := s.out[sub]
	if len(q) == 0 {
		return proc.Result{}, nil
	}
	s.out[sub] = q[1:]
	return proc.Result{Stdout: []byte(q[0])}, nil
}

// TestRespawnMapsRespawnFailures pins the re-read after a refused
// respawn-pane call: a pane that came alive under the send reads
// PANE_BUSY, a gone pane PANE_NOT_FOUND, and a still-dead pane INTERNAL.
// No message matches tmux's error text, so no tmux version can skew it.
func TestRespawnMapsRespawnFailures(t *testing.T) {
	t.Parallel()
	start := startID(1000, 2000)
	pane := &typev1.PaneKey{Server: "default", ServerStartId: start, PaneId: "%1"}
	for _, tc := range []struct {
		name   string
		relist string
		status typev1.Status
		reason typev1.ReasonCode
	}{
		{"pane alive under the respawn", "%1 0 sleep", typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_PANE_BUSY},
		{"pane gone under the respawn", "%2 0 sleep", typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_PANE_NOT_FOUND},
		{"pane still dead", "%1 1 sh", typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INTERNAL},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			o := testOptions()
			o.Runner = &seqRunner{
				out: map[string][]string{
					"display-message": {"1000 2000", "1000 2000"},
					"list-panes":      {"%1 1 sh", tc.relist},
				},
				err: map[string]error{"respawn-pane": errStubRespawn},
			}
			e, err := New(o)
			if err != nil {
				t.Fatal(err)
			}
			res := e.RespawnPane(t.Context(), &agentv1.RespawnPane{Pane: pane})
			if res.GetStatus() != tc.status || res.GetReason() != tc.reason {
				t.Fatalf("RespawnPane = (%v, %v), want (%v, %v): %q",
					res.GetStatus(), res.GetReason(), tc.status, tc.reason, res.GetMessage())
			}
		})
	}
}
