package gateway_test

import (
	"context"
	"database/sql"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/gateway"
)

// unhandledMsg is a message of an arm with no product handler yet
// (SettingsAck's comes with plan row 5h).
func unhandledMsg() *agentv1.ConnectRequest {
	return &agentv1.ConnectRequest{Body: &agentv1.ConnectRequest_SettingsAck{SettingsAck: &agentv1.SettingsAck{}}}
}

func agentBye() *agentv1.ConnectRequest {
	return &agentv1.ConnectRequest{Body: &agentv1.ConnectRequest_Bye{Bye: &agentv1.Bye{
		Reason: agentv1.ByeReason_BYE_REASON_SHUTDOWN,
	}}}
}

// shutdown runs the listener's Shutdown and fails the test unless it ends
// well inside fleetd's 10 s grace.
func (h *hub) shutdown(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()
	if err := h.srv.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("Shutdown took %v, want well under the 10 s grace", took)
	}
}

// closedLines are the reconnect.closed lines with err_kind kind.
func (h *hub) closedLines(t *testing.T, kind string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, l := range h.events(t, "reconnect.closed") {
		if l["err_kind"] == kind {
			out = append(out, l)
		}
	}
	return out
}

// Messages the agent sent before its Bye are handled even when the Bye
// is read before the ingest loop got to them: the stream ends only after
// all three are counted.
func TestQueuedMessagesBeforeTheAgentsByeAreAllHandled(t *testing.T) {
	t.Parallel()
	h := startHub(t)
	m := h.enrollMachine(t)
	s := h.open(t, m)
	s.send(t, hello(m, 0))
	s.ack(t)
	const burst = 3
	for range burst {
		s.send(t, unhandledMsg())
	}
	s.send(t, agentBye())
	if err := s.ended(t); err != nil {
		t.Fatalf("stream end %v, want clean", err)
	}
	if v := sample(scrape(t, h.fixture), "fleet_hub_connect_unhandled_total", `kind="settings_ack"`); v != burst {
		t.Errorf("fleet_hub_connect_unhandled_total{kind=event} = %v when the stream ended, want %d", v, burst)
	}
}

// The same with an arm handler slow enough that the Bye is always read
// while messages are still queued: every one is handled before the
// stream ends.
func TestQueuedMessagesBeforeTheAgentsByeAreHandledBehindASlowArm(t *testing.T) {
	t.Parallel()
	var handled atomic.Int64
	h := startHub(t, func(o *gateway.ServerOptions) {
		gateway.SetArm(o, "settings_ack", func() {
			time.Sleep(20 * time.Millisecond)
			handled.Add(1)
		})
	})
	m := h.enrollMachine(t)
	s := h.open(t, m)
	s.send(t, hello(m, 0))
	s.ack(t)
	// Ten, so the reader's end is almost surely seen with some still queued.
	const burst = 10
	for range burst {
		s.send(t, unhandledMsg())
	}
	s.send(t, agentBye())
	if err := s.ended(t); err != nil {
		t.Fatalf("stream end %v, want clean", err)
	}
	if n := handled.Load(); n != burst {
		t.Errorf("%d events handled when the stream ended, want %d", n, burst)
	}
}

// A stream with a certificate that never said Hello ends with
// Bye{HUB_RESTART} at Shutdown, which does not wait out its grace.
func TestShutdownEndsAStreamThatNeverSaidHello(t *testing.T) {
	t.Parallel()
	h := startHub(t)
	m := h.enrollMachine(t)
	s := h.open(t, m)
	_ = s.bs.Send(nil) // headers only
	time.Sleep(100 * time.Millisecond)
	h.shutdown(t)
	if got := s.bye(t); got != agentv1.ByeReason_BYE_REASON_HUB_RESTART {
		t.Errorf("Bye %v, want HUB_RESTART", got)
	}
	if lines := h.closedLines(t, "shutdown"); len(lines) != 1 || lines[0]["machine_id"] != m.id {
		t.Errorf("reconnect.closed{err_kind=shutdown} lines %v, want one for the machine", lines)
	}
	if v := sample(scrape(t, h.fixture), "fleet_hub_connect_closed_before_hello_total", `err_kind="shutdown"`); v != 1 {
		t.Errorf("fleet_hub_connect_closed_before_hello_total{err_kind=shutdown} = %v, want 1", v)
	}
}

// A Hello whose writer job is stuck behind another job ends with
// Bye{HUB_RESTART} at Shutdown instead of holding it for the grace. Its
// job may still commit later (an unknown outcome; the next Hello displaces
// the row it leaves).
func TestShutdownEndsAHelloWaitingForTheStore(t *testing.T) {
	t.Parallel()
	h := startHub(t)
	m := h.enrollMachine(t)
	release := make(chan struct{})
	held, err := h.store.Submit(context.Background(), func(*sql.Tx) error { <-release; return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer func() { close(release); <-held }()
	s := h.open(t, m)
	s.send(t, hello(m, 0))
	// The held job's batch takes the Hello's job too; give the Hello time
	// to reach statedb.Hello (setup only: had it not, the shutdown would
	// still end it, in firstMessage).
	time.Sleep(500 * time.Millisecond)
	h.shutdown(t)
	if got := s.bye(t); got != agentv1.ByeReason_BYE_REASON_HUB_RESTART {
		t.Errorf("Bye %v, want HUB_RESTART", got)
	}
	if lines := h.closedLines(t, "shutdown"); len(lines) != 1 {
		t.Errorf("reconnect.closed{err_kind=shutdown} lines %v, want one", lines)
	}
}

// A Hello that commits after Shutdown began gets Bye{HUB_RESTART} and its
// new row is closed as hub_restart at once.
func TestAHelloCommittedDuringShutdownClosesItsRow(t *testing.T) {
	t.Parallel()
	var srv atomic.Pointer[gateway.Server]
	h := startHub(t, func(o *gateway.ServerOptions) {
		gateway.SetAfterHelloCommit(o, func() { gateway.BeginShutdown(srv.Load()) })
	})
	srv.Store(h.srv)
	m := h.enrollMachine(t)
	s := h.open(t, m)
	s.send(t, hello(m, 0))
	if got := s.bye(t); got != agentv1.ByeReason_BYE_REASON_HUB_RESTART {
		t.Errorf("Bye %v, want HUB_RESTART", got)
	}
	h.waitConns(t, m.id, "the hub_restart row", func(c []conn) bool { return len(c) == 1 && c[0].endReason == "hub_restart" })
	h.notAccepted(t)
}

// notAccepted checks that no Hello was logged or counted as accepted and
// that the stream's end was logged as hub_restart.
func (h *hub) notAccepted(t *testing.T) {
	t.Helper()
	if lines := h.events(t, "reconnect.hello"); len(lines) != 0 {
		t.Errorf("reconnect.hello lines %v, want none: no HelloAck was queued", lines)
	}
	if v := sample(scrape(t, h.fixture), "fleet_hub_reconnects_total"); v > 0 {
		t.Errorf("fleet_hub_reconnects_total = %v, want none", v)
	}
	end := time.Now().Add(10 * time.Second)
	for {
		lines := h.events(t, "reconnect.closed")
		if len(lines) == 1 && lines[0]["reason"] == "hub_restart" {
			return
		}
		if time.Now().After(end) {
			t.Fatalf("reconnect.closed lines %v, want one hub_restart", lines)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A stream the shutdown ends right after it is registered queues no
// HelloAck: it gets Bye{HUB_RESTART}, its row closes as hub_restart, and
// it is not logged or counted as an accepted Hello.
func TestAStreamEndedBeforeItsHelloAckIsNotAccepted(t *testing.T) {
	t.Parallel()
	var srv atomic.Pointer[gateway.Server]
	h := startHub(t, func(o *gateway.ServerOptions) {
		gateway.SetAfterRegister(o, func() { gateway.BeginShutdown(srv.Load()) })
	})
	srv.Store(h.srv)
	m := h.enrollMachine(t)
	s := h.open(t, m)
	s.send(t, hello(m, 0))
	if got := s.bye(t); got != agentv1.ByeReason_BYE_REASON_HUB_RESTART {
		t.Errorf("Bye %v, want HUB_RESTART", got)
	}
	h.waitConns(t, m.id, "the hub_restart row", func(c []conn) bool { return len(c) == 1 && c[0].endReason == "hub_restart" })
	h.notAccepted(t)
}

// The rate limit runs after the certificate, message, machine_id, field
// and proto checks: Hellos those refuse do not start its window, so a good
// Hello at the same instant is accepted.
func TestRefusalsBeforeTheLimitDoNotStartItsWindow(t *testing.T) {
	t.Parallel()
	h := startHub(t)
	m := h.enrollMachine(t)
	bad := []func(*agentv1.Hello){
		func(h *agentv1.Hello) { h.ProtoMin, h.ProtoMax = 2, 3 },
		func(h *agentv1.Hello) { h.GetMachineId().Value = "01OTHER" },
		func(h *agentv1.Hello) { h.BootId = "" },
	}
	for i, set := range bad {
		s := h.open(t, m)
		s.send(t, withHello(set)(m))
		select {
		case <-s.msgs: // the proto refusal is a Bye
		case <-s.end:
		case <-time.After(10 * time.Second):
			t.Fatalf("bad Hello %d: no answer", i)
		}
	}
	s := h.open(t, m)
	s.send(t, hello(m, 0))
	if ep := s.ack(t).GetEpoch(); ep != 1 {
		t.Errorf("epoch %d, want 1", ep)
	}
	if v := sample(scrape(t, h.fixture), "fleet_hub_connect_refused_total", `cause="rate_limited"`); v > 0 {
		t.Errorf("fleet_hub_connect_refused_total{cause=rate_limited} = %v, want none", v)
	}
}

// A stream the agent left with Bye is gone from the registry: the next
// Hello for the machine counts no duplicate.
// Bye.message is dropped with the stream (02e): a Bye carrying a secret
// ends the stream as agent_bye, and the message is never stored or logged.
func TestAnAgentByeWithAMessageIsDroppedWithTheStream(t *testing.T) {
	t.Parallel()
	h := startHub(t)
	m := h.enrollMachine(t)
	s := h.open(t, m)
	s.send(t, hello(m, 0))
	s.ack(t)
	secret := "ghp_0123456789abcdefghijABCDEFGHIJ012345"
	bye := agentBye()
	bye.GetBye().Message = "leaving: token " + secret
	s.send(t, bye)
	if err := s.ended(t); err != nil {
		t.Fatal(err)
	}
	h.waitConns(t, m.id, "the agent_bye row", func(c []conn) bool {
		return len(c) == 1 && c[0].endReason == "agent_bye"
	})
	if logs := h.logs.String(); strings.Contains(logs, secret) {
		t.Errorf("a log line carries the Bye message's secret")
	}
}

func TestAnAgentByeLeavesNoStreamForTheNextHello(t *testing.T) {
	t.Parallel()
	h := startHub(t)
	m := h.enrollMachine(t)
	first := h.open(t, m)
	first.send(t, hello(m, 0))
	first.ack(t)
	first.send(t, agentBye())
	if err := first.ended(t); err != nil {
		t.Fatal(err)
	}
	h.clock.set(h.clock.now().Add(6 * time.Second))
	second := h.open(t, m)
	second.send(t, hello(m, 1))
	second.ack(t)
	if n := len(h.events(t, "identity.duplicate")); n != 0 {
		t.Errorf("%d identity.duplicate lines, want 0", n)
	}
	if v := sample(scrape(t, h.fixture), "fleet_hub_duplicate_identity_total"); v > 0 {
		t.Errorf("fleet_hub_duplicate_identity_total = %v, want none", v)
	}
}

// A first message over the cap ends the stream RESOURCE_EXHAUSTED, logged
// as reconnect.closed{err_kind=oversize} and counted.
func TestAnOversizeFirstMessageIsLoggedAndCounted(t *testing.T) {
	t.Parallel()
	h := startHub(t)
	m := h.enrollMachine(t)
	s := h.open(t, m)
	r := hello(m, 0)
	r.GetHello().AgentVersion = strings.Repeat("v", gateway.MaxMessageBytes)
	_ = s.bs.Send(r)
	if err := s.ended(t); connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Errorf("stream end %v, want resource_exhausted", err)
	}
	if lines := h.closedLines(t, "oversize"); len(lines) != 1 || lines[0]["machine_id"] != m.id {
		t.Errorf("reconnect.closed{err_kind=oversize} lines %v, want one", lines)
	}
	if v := sample(scrape(t, h.fixture), "fleet_hub_connect_closed_before_hello_total", `err_kind="oversize"`); v != 1 {
		t.Errorf("fleet_hub_connect_closed_before_hello_total{err_kind=oversize} = %v, want 1", v)
	}
}

// An agent that goes away before its Hello is logged as
// reconnect.closed{err_kind=reset} and counted.
func TestAStreamResetBeforeHelloIsLoggedAndCounted(t *testing.T) {
	t.Parallel()
	h := startHub(t)
	m := h.enrollMachine(t)
	s := h.open(t, m)
	_ = s.bs.Send(nil)
	time.Sleep(100 * time.Millisecond)
	s.cancel()
	h.waitMetric(t, "fleet_hub_connect_closed_before_hello_total", 1, `err_kind="reset"`)
	if lines := h.closedLines(t, "reset"); len(lines) != 1 {
		t.Errorf("reconnect.closed{err_kind=reset} lines %v, want one", lines)
	}
}

// holdWriter blocks the store's writer until the returned func runs (at
// most once; the test's end runs it too).
func (h *hub) holdWriter(t *testing.T) func() {
	t.Helper()
	release := make(chan struct{})
	held, err := h.store.Submit(context.Background(), func(*sql.Tx) error { <-release; return nil })
	if err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	free := func() { once.Do(func() { close(release); <-held }) }
	t.Cleanup(free)
	return free
}

// The row close after a Bye may take longer than the Bye's 2 s write
// deadline (a busy store): the stream still ends cleanly after the Bye.
func TestASlowRowCloseAfterTheByeStillEndsTheStreamCleanly(t *testing.T) {
	t.Parallel()
	h := startHub(t)
	m := h.enrollMachine(t)
	s := h.open(t, m)
	s.send(t, hello(m, 0))
	s.ack(t)
	free := h.holdWriter(t)
	stopped := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		stopped <- h.srv.Shutdown(ctx)
	}()
	m1 := s.next(t)
	if m1.GetBye().GetReason() != agentv1.ByeReason_BYE_REASON_HUB_RESTART {
		t.Fatalf("message %v, want Bye{HUB_RESTART}", m1)
	}
	time.Sleep(2500 * time.Millisecond) // past the Bye's write deadline
	free()
	if err := s.ended(t); err != nil {
		t.Errorf("stream end %v after the Bye, want clean", err)
	}
	if err := <-stopped; err != nil {
		t.Errorf("Shutdown: %v", err)
	}
	h.waitConns(t, m.id, "the hub_restart row", func(c []conn) bool { return len(c) == 1 && c[0].endReason == "hub_restart" })
}

// An agent that leaves while its Hello waits for the store is a departure
// (reconnect.closed{err_kind=reset}, info), not the hub's failure.
func TestAnAgentLeavingWhileItsHelloWaitsIsADeparture(t *testing.T) {
	t.Parallel()
	h := startHub(t)
	m := h.enrollMachine(t)
	free := h.holdWriter(t)
	s := h.open(t, m)
	s.send(t, hello(m, 0))
	time.Sleep(300 * time.Millisecond) // setup: the Hello reaches its job wait
	s.cancel()
	h.waitMetric(t, "fleet_hub_connect_closed_before_hello_total", 1, `err_kind="reset"`)
	free()
	if lines := h.events(t, "connect.failed"); len(lines) != 0 {
		t.Errorf("connect.failed lines %v, want none for an agent that left", lines)
	}
	if lines := h.closedLines(t, "reset"); len(lines) != 1 || lines[0]["level"] != "info" {
		t.Errorf("reconnect.closed{err_kind=reset} lines %v, want one at info", lines)
	}
}
