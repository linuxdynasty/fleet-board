package conn_test

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
	"github.com/linuxdynasty/fleet-board/internal/agent/conn/conntest"
)

// enrolledAgent enrolls a new agent with h and returns it with its
// machine id; its loop is not started.
func enrolledAgent(t *testing.T, h *conntest.Hub) (*agent, string) {
	t.Helper()
	a := newAgent(t)
	if _, err := a.enroll(t, h.Addr, h.Token(t, time.Now().Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
	ident, err := conn.LoadIdentity(a.dir)
	if err != nil {
		t.Fatal(err)
	}
	return a, ident.MachineID
}

// commandRow is machine's row of id on h.
func commandRow(t *testing.T, h *conntest.Hub, machine, id string) conntest.CommandRow {
	t.Helper()
	for _, c := range h.Commands(t, machine) {
		if c.CommandID == id {
			return c
		}
	}
	t.Fatalf("no row for %s", id)
	return conntest.CommandRow{}
}

// A command whose deadline passed while its machine was away is expired in
// the Snapshot transaction of the machine's next connection (row 5
// ruling, fork 1: 03n "apply snapshot · machine ONLINE · expire outbox
// rows past not_after_time"), before anything is sent, so it never
// reaches the agent; one still inside its deadline goes out and is done.
// Nothing but the Snapshot runs the expiry here: no housekeeping pass.
func TestTheRealHubExpiresADueCommandInTheSnapshotTransaction(t *testing.T) {
	t.Parallel()
	hubClock := &skewClock{}
	h := conntest.StartHub(t, conntest.Options{Now: hubClock.now})
	a, id := enrolledAgent(t, h)
	due := interruptCmd("c-due")
	due.NotAfterTime = timestamppb.New(hubClock.now().Add(time.Minute))
	h.Enqueue(t, id, due, interruptCmd("c-ok"))
	hubClock.set(2 * time.Minute) // c-due's deadline passed while the machine was away

	x := &testExecutor{db: a.openDB(t)}
	r := startLoopThen(t, a, func(o *conn.LoopOptions) { o.Command = x.hook }, func(l *conn.Loop) { x.loop = l })
	done(t, h, a, id, "c-ok")
	if got := commandRow(t, h, id, "c-due"); got.State != "expired" || got.OpState != "DONE" || got.Status != "EXPIRED" ||
		got.Reason != "EXPIRED" || got.SentEpoch != 0 || got.Acked {
		t.Errorf("c-due %+v, want expired, never sent", got)
	}
	ev := h.Events(t, "outbox.expired")
	if len(ev) != 1 || ev[0]["command_id"] != "c-due" || ev[0]["cause"] != "deadline" || ev[0]["machine_id"] != id {
		t.Errorf("outbox.expired %v, want one line for c-due", ev)
	}
	logs := h.LogText()
	if applied, expired := strings.Index(logs, `"msg":"snapshot.applied"`), strings.Index(logs, `"msg":"outbox.expired"`); applied < 0 ||
		expired < applied {
		t.Errorf("outbox.expired at %d, snapshot.applied at %d: the expiry is logged after the Snapshot committed", expired, applied)
	}
	x.wg.Wait()
	if got := x.got(); !slices.Equal(got, []string{"c-ok epoch 1 seq 1"}) {
		t.Errorf("the agent got %q, want only c-ok, as seq 1", got)
	}
	r.running(t)
}

// A command the agent took (delivered) whose deadline comes with no
// result is expired by housekeeping (02i: delivered --> expired). Its
// result, when it comes late, is answered with ResultAck (so the agent
// stops re-sending it) and ignored: no result is stored and the row stays
// expired (03n "Results for commands the hub already closed are
// ignored").
func TestTheRealHubExpiresADeliveredCommandAndIgnoresItsLateResult(t *testing.T) {
	t.Parallel()
	hubClock := &skewClock{}
	h := conntest.StartHub(t, conntest.Options{Now: hubClock.now})
	a, id := enrolledAgent(t, h)
	release := newGate()
	x := &testExecutor{db: a.openDB(t), hold: release}
	r := startLoopThen(t, a, func(o *conn.LoopOptions) { o.Command = x.hook }, func(l *conn.Loop) { x.loop = l })
	r.connected(t)
	cmd := interruptCmd("c-late")
	cmd.NotAfterTime = timestamppb.New(hubClock.now().Add(time.Minute))
	h.Enqueue(t, id, cmd)
	waitFor(t, 10*time.Second, "c-late delivered", func() bool { return commandRow(t, h, id, "c-late").State == "delivered" })

	h.ExpireCommands(t, hubClock.now().Add(2*time.Minute))
	if got := commandRow(t, h, id, "c-late"); got.State != "expired" || got.Status != "EXPIRED" || !got.Acked {
		t.Fatalf("c-late %+v, want expired after its deadline", got)
	}
	if ev := h.Events(t, "outbox.expired"); len(ev) != 1 || ev[0]["from"] != "delivered" {
		t.Errorf("outbox.expired %v, want one from delivered", ev)
	}
	release.open()
	waitFor(t, 10*time.Second, "the ResultAck of c-late in agent.db", func() bool {
		e, err := a.openDB(t).Command(context.Background(), "c-late")
		return err == nil && !e.Acked.IsZero()
	})
	if got := commandRow(t, h, id, "c-late"); got.State != "expired" {
		t.Errorf("c-late is %s after its late result, want expired", got.State)
	}
	if res := h.Results(t, id); len(res) != 0 {
		t.Errorf("results stored for an expired command: %+v", res)
	}
	x.wg.Wait()
	x.mu.Lock()
	defer x.mu.Unlock()
	if len(x.errs) != 0 {
		t.Errorf("the executor failed: %v", x.errs)
	}
	r.running(t)
}
