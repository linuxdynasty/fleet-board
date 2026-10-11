package exec_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
	"github.com/linuxdynasty/fleet-board/internal/agent/conn/conntest"
	"github.com/linuxdynasty/fleet-board/internal/agent/exec"
)

// The 05b lines come from the production path with ids, kinds and enum
// names only: never the result message (which may hold screen text).
func TestCommandLogLinesCarryIDsNotText(t *testing.T) {
	t.Parallel()
	h := capturePane("tmux.observe")
	h.block = make(chan struct{})
	h.result = &agentv1.CommandResult{Status: typev1.Status_STATUS_OK, Message: "secret-looking screen text"}
	r := newRig(t, h)
	r.start(t)
	r.give(t, cmd("t-1", 1, "tmux.observe"))
	waitFor(t, func() bool { return h.runs.Load() == 1 })
	if got := r.metric(t, "fleet_agent_commands_running", ""); got != "1" {
		t.Errorf("commands_running while one runs: %q, want 1", got)
	}
	close(h.block)
	r.result(t, "t-1")
	r.give(t, cmd("t-2", 2, "tmux.control")) // refused: UNSUPPORTED
	r.result(t, "t-2")
	waitFor(t, func() bool { return r.metric(t, "fleet_agent_commands_running", "") == "0" })

	received := conntest.Lines(t, r.logs.String(), "command.received")
	if len(received) != 2 || received[0]["command_id"] != "t-1" || received[0]["kind"] != "CapturePane" ||
		received[0]["epoch"] != float64(1) || received[0]["seq"] != float64(1) {
		t.Errorf("command.received lines %v", received)
	}
	finished := conntest.Lines(t, r.logs.String(), "command.finished")
	if len(finished) != 1 || finished[0]["status"] != "OK" || finished[0]["elapsed_ms"] == nil {
		t.Errorf("command.finished lines %v, want one OK with elapsed_ms", finished)
	}
	refused := conntest.Lines(t, r.logs.String(), "command.refused")
	if len(refused) != 1 || refused[0]["command_id"] != "t-2" || refused[0]["status"] != "UNSUPPORTED" {
		t.Errorf("command.refused lines %v, want t-2 UNSUPPORTED", refused)
	}
	if strings.Contains(r.logs.String(), "secret-looking") {
		t.Error("a log line carries the result message")
	}
	if got := r.metric(t, "fleet_agent_command_seconds_count", `kind="CapturePane"`); got != "1" {
		t.Errorf("command_seconds count %q, want 1 (refusals are not timed)", got)
	}
	if got := r.metric(t, "fleet_agent_commands_total", `status="UNSUPPORTED"`); got != "1" {
		t.Errorf("commands_total{UNSUPPORTED} %q, want 1", got)
	}
}

// A command that carried no trace gets a result with none (command.proto).
func TestNoTraceInNoTraceOut(t *testing.T) {
	t.Parallel()
	r := newRig(t, capturePane("tmux.observe"))
	r.start(t)
	r.give(t, cmd("n-1", 1, "tmux.observe"))
	if tr := r.result(t, "n-1").GetTrace(); tr != nil {
		t.Errorf("result trace %v, want unset", tr)
	}
}

// The hook never blocks the sending goroutine: a full intake refuses, and
// the connection loop drops the command for the hub to re-send.
func TestAFullIntakeRefusesWithoutBlocking(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ex, err := exec.New(exec.Options{DB: r.db, Sender: r.send, Clock: r.clock, Policy: r.pol, Intake: 2})
	if err != nil {
		t.Fatal(err)
	}
	ok := 0
	for i := range 3 {
		if ex.Command(context.Background(), 1, cmd("i", uint64(i+1), "x")) {
			ok++
		}
	}
	if ok != 2 {
		t.Errorf("the intake took %d of 3 with room for 2", ok)
	}
}

// A command_id that cannot be logged or answered is dropped, and nothing
// is logged, acked or sent.
func TestAnInvalidCommandIDIsDropped(t *testing.T) {
	t.Parallel()
	r := newRig(t, capturePane("tmux.observe"))
	r.start(t)
	for _, id := range []string{"", "bad id", strings.Repeat("x", 129)} {
		r.give(t, cmd(id, 1, "tmux.observe"))
	}
	r.give(t, cmd("after", 2, "tmux.observe")) // the intake is ordered: once this is answered, the rest were handled
	r.result(t, "after")
	if n := len(r.send.all()); n != 2 {
		t.Errorf("sent %d messages, want only the ack and result of the valid command", n)
	}
	if got := conntest.Lines(t, r.logs.String(), "command.dropped"); len(got) != 3 || got[0]["reason"] != "invalid_command_id" {
		t.Errorf("command.dropped lines %v, want 3", got)
	}
}

func TestNewRefusesBadOptions(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	base := exec.Options{DB: r.db, Sender: r.send, Clock: r.clock, Policy: r.pol}
	for _, tc := range []struct {
		name string
		edit func(o *exec.Options)
	}{
		{"no store", func(o *exec.Options) { o.DB = nil }},
		{"no sender", func(o *exec.Options) { o.Sender = nil }},
		{"no clock", func(o *exec.Options) { o.Clock = nil }},
		{"no policy", func(o *exec.Options) { o.Policy = nil }},
		{"unknown kind", func(o *exec.Options) { o.Handlers = []exec.Handler{&handler{kind: "RunExec"}} }},
		{"two handlers", func(o *exec.Options) {
			o.Handlers = []exec.Handler{capturePane("a"), capturePane("b")}
		}},
	} {
		o := base
		tc.edit(&o)
		if _, err := exec.New(o); err == nil {
			t.Errorf("%s: New accepted it", tc.name)
		}
	}
}

// A capability below the version the command requires is not enough.
func TestRequiresChecksTheVersion(t *testing.T) {
	t.Parallel()
	h := capturePane("tmux.observe")
	r := newRig(t, h)
	r.start(t)
	c := cmd("v-1", 1, "tmux.observe")
	c.Requires.Version = 2
	c.NotAfterTime = timestamppb.New(hub0.Add(5 * time.Minute))
	r.give(t, c)
	if res := r.result(t, "v-1"); res.GetStatus() != typev1.Status_STATUS_UNSUPPORTED || h.runs.Load() != 0 {
		t.Errorf("result %v, want UNSUPPORTED for a capability below the required version", res)
	}
}

// refusing is a connection loop that refuses every message for a reason
// other than "no stream".
type refusing struct{}

func (refusing) Send(context.Context, *agentv1.ConnectRequest) error { return conn.ErrResultNotLogged }

// A send the loop refuses is logged with the command's id and kind, so the
// 3 AM reader knows which command did not reach the hub.
func TestASendFailureNamesTheCommand(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ex, err := exec.New(exec.Options{
		DB: r.db, Sender: refusing{}, Clock: r.clock, Policy: r.pol, Telemetry: r.tel,
		Handlers: []exec.Handler{capturePane("tmux.observe")},
	})
	if err != nil {
		t.Fatal(err)
	}
	r.ex = ex
	r.start(t)
	r.give(t, cmd("sf-1", 1, "tmux.observe"))
	waitFor(t, func() bool { return len(conntest.Lines(t, r.logs.String(), "command.send_failed")) >= 2 })
	for _, l := range conntest.Lines(t, r.logs.String(), "command.send_failed") {
		if l["command_id"] != "sf-1" || l["kind"] != "CapturePane" || l["err_kind"] != "not_logged" {
			t.Errorf("command.send_failed %v, want command_id sf-1, kind CapturePane, err_kind not_logged", l)
		}
	}
}
