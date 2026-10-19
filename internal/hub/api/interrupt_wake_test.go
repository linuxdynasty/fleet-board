package api_test

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/api"
	"github.com/linuxdynasty/fleet-board/internal/hub/outbox"
)

type interruptWake struct {
	op      outbox.Op
	visible bool
	err     error
}

// interruptWakeProbe reads through a separate connection. Seeing the row in
// this callback proves that it ran after the writer committed the command.
func interruptWakeProbe(e **fleetEnv, wakes chan<- interruptWake) func(outbox.Op) {
	return func(op outbox.Op) {
		if op.Kind != "interrupt" {
			return
		}
		w := interruptWake{op: op}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		w.err = (*e).st.Read(ctx, func(tx *sql.Tx) error {
			var count int
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM commands WHERE command_id = ?`,
				op.CommandID).Scan(&count); err != nil {
				return err
			}
			w.visible = count == 1
			return nil
		})
		wakes <- w
	}
}

func interruptWakeRequest(session, requestID string) *hubv1.InterruptPaneRequest {
	return &hubv1.InterruptPaneRequest{
		RequestId: requestID,
		Session:   &typev1.SessionId{Value: session},
		Pane:      &typev1.PaneKey{Server: "default", ServerStartId: "s1", PaneId: "%1"},
	}
}

func takeInterruptWake(t *testing.T, wakes <-chan interruptWake) interruptWake {
	t.Helper()
	select {
	case w := <-wakes:
		if w.err != nil || !w.visible || !w.op.Created {
			t.Fatalf("wake before committed new row: %+v", w)
		}
		return w
	default:
		t.Fatal("new committed Interrupt had no wake")
		return interruptWake{}
	}
}

func noInterruptWake(t *testing.T, wakes <-chan interruptWake) {
	t.Helper()
	select {
	case w := <-wakes:
		t.Fatalf("unexpected Interrupt wake: %+v", w)
	default:
	}
}

func awaitWriterQueue(t *testing.T, e *fleetEnv, depth float64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if got := sample(e.scrape(t), "fleet_hub_writer_queue_depth", `db="state"`); got >= depth {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("writer queue never reached depth %.0f", depth)
}

// TestInterruptWakeAfterCancelledRequestCommit uses two ordinary store jobs
// as barriers. The API job is admitted behind the first, then a following
// job cancels its request after the API SQL callback but before COMMIT. The
// RPC must still await the definitive writer result and wake exactly once.
func TestInterruptWakeAfterCancelledRequestCommit(t *testing.T) {
	var e *fleetEnv
	wakes := make(chan interruptWake, 2)
	e = newFleetEnv(t, localUser("alice"), withCommandQueued(interruptWakeProbe(&e, wakes)))
	enrollMachine(t, e, "m1")
	session := createSession(t, e, "m1", "wake-cancel")
	req := interruptWakeRequest(session, "req-interrupt-wake-cancel")
	before := pendingCommands(t, e, "m1")

	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	var releaseFirstOnce sync.Once
	defer releaseFirstOnce.Do(func() { close(releaseFirst) })
	firstDone, err := e.st.Submit(context.Background(), func(*sql.Tx) error {
		close(firstEntered)
		<-releaseFirst
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-firstEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("first writer job did not start")
	}

	ctx, cancel := context.WithCancel(api.WithPrincipal(context.Background(), localUser("alice")))
	defer cancel()
	type callResult struct {
		response *connect.Response[hubv1.InterruptPaneResponse]
		err      error
	}
	called := make(chan callResult, 1)
	go func() {
		response, err := e.fleet.InterruptPane(ctx, connect.NewRequest(req))
		called <- callResult{response, err}
	}()
	awaitWriterQueue(t, e, 1)

	cancelled := make(chan struct{})
	releaseCommit := make(chan struct{})
	var releaseCommitOnce sync.Once
	defer releaseCommitOnce.Do(func() { close(releaseCommit) })
	cancelDone, err := e.st.Submit(context.Background(), func(*sql.Tx) error {
		cancel()
		close(cancelled)
		<-releaseCommit
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	releaseFirstOnce.Do(func() { close(releaseFirst) })
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("post-API cancellation job did not run")
	}
	noInterruptWake(t, wakes)
	var early *callResult
	select {
	case result := <-called:
		early = &result
	case <-time.After(50 * time.Millisecond):
	}
	releaseCommitOnce.Do(func() { close(releaseCommit) })
	if err := <-cancelDone; err != nil {
		t.Fatal(err)
	}
	if early != nil {
		t.Fatalf("InterruptPane returned before commit: %v", early.err)
	}
	var result callResult
	select {
	case result = <-called:
	case <-time.After(5 * time.Second):
		t.Fatal("InterruptPane did not finish after commit")
	}
	if result.err != nil {
		t.Fatalf("committed InterruptPane: %v", result.err)
	}
	w := takeInterruptWake(t, wakes)
	if got := result.response.Msg.GetOperation().GetId().GetValue(); got != w.op.CommandID {
		t.Fatalf("operation id = %q, wake id = %q", got, w.op.CommandID)
	}
	if got := pendingCommands(t, e, "m1"); got != before+1 {
		t.Fatalf("pending commands = %d, want %d", got, before+1)
	}
	replay, err := e.client.InterruptPane(context.Background(), connect.NewRequest(req))
	if err != nil {
		t.Fatalf("replay InterruptPane: %v", err)
	}
	if got := replay.Msg.GetOperation().GetId().GetValue(); got != w.op.CommandID {
		t.Fatalf("replay operation id = %q, want %q", got, w.op.CommandID)
	}
	if got := pendingCommands(t, e, "m1"); got != before+1 {
		t.Fatalf("replay made %d pending commands, want %d", got, before+1)
	}
	noInterruptWake(t, wakes)
}

// TestInterruptWakeOnlyForCommittedNewRows fails the audit insert after the
// command was enqueued inside sendOne. The savepoint must remove the command
// and the callback must stay silent; a retry then commits and wakes once.
func TestInterruptWakeOnlyForCommittedNewRows(t *testing.T) {
	var e *fleetEnv
	wakes := make(chan interruptWake, 2)
	e = newFleetEnv(t, localUser("alice"), withCommandQueued(interruptWakeProbe(&e, wakes)))
	enrollMachine(t, e, "m1")
	session := createSession(t, e, "m1", "wake-rollback")
	req := interruptWakeRequest(session, "req-interrupt-wake-rollback")
	before := pendingCommands(t, e, "m1")

	if err := e.st.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`CREATE TRIGGER fail_interrupt_audit BEFORE INSERT ON audit_log
			WHEN NEW.action = 'session.interrupt'
			BEGIN SELECT RAISE(ABORT, 'injected after enqueue'); END`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.client.InterruptPane(context.Background(), connect.NewRequest(req)); err == nil {
		t.Fatal("InterruptPane succeeded despite audit rollback")
	}
	if got := pendingCommands(t, e, "m1"); got != before {
		t.Fatalf("rolled-back call left %d commands, want %d", got, before)
	}
	noInterruptWake(t, wakes)
	if err := e.st.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`DROP TRIGGER fail_interrupt_audit`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		res, err := e.client.InterruptPane(context.Background(), connect.NewRequest(req))
		if err != nil {
			t.Fatalf("InterruptPane attempt %d: %v", i+1, err)
		}
		if i == 0 {
			w := takeInterruptWake(t, wakes)
			if got := res.Msg.GetOperation().GetId().GetValue(); got != w.op.CommandID {
				t.Fatalf("operation id = %q, wake id = %q", got, w.op.CommandID)
			}
		}
		if got := pendingCommands(t, e, "m1"); got != before+1 {
			t.Fatalf("attempt %d left %d commands, want %d", i+1, got, before+1)
		}
		noInterruptWake(t, wakes)
	}
	if got := len(auditActionsFor(t, e, fmt.Sprintf("sessions/%s", session))); got != 2 {
		t.Fatalf("audit actions = %d, want create + one interrupt", got)
	}
}
