package native_test

import (
	"context"
	"testing"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/native"
)

// A duplicate command_id returns the first outcome and runs the adapter
// once: the redelivery may come from a lost ack, never from a second intent.
func TestHostDuplicateCommandReturnsFirstOutcome(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	startSession(t, f, startCmd("command-start"))

	send := sendCmd("command-send", "session-1", 1, "first")
	loggedCommand(t, f.db, send)
	first := f.h.Run(ctx, send)
	if first.GetStatus() != typev1.Status_STATUS_OK {
		t.Fatalf("first send: %+v", first)
	}
	// Same id, same bytes, as if the ack was lost: replay, no second turn.
	second := f.h.Run(ctx, send)
	if second.GetStatus() != typev1.Status_STATUS_OK {
		t.Fatalf("duplicate send: %+v", second)
	}
	if got := second.GetSendNativeTurn(); got.GetEffectId() != "command-send" || got.GetAdmissionOrder() != 2 {
		t.Fatalf("duplicate detail %+v, want the first admission (start is order 1)", got)
	}
	if calls := f.fa.started(); len(calls) != 2 { // start + one send
		t.Fatalf("adapter calls %v, want exactly one send", calls)
	}
	if n := countEffects(t, f, ""); n != 2 {
		t.Fatalf("journal holds %d effects, want start + one send", n)
	}
}

// A duplicate start id after confirmation rebuilds the bound session, with
// no second provider session.
func TestHostDuplicateStartRebuildsBinding(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	cmd := startCmd("command-start")
	loggedCommand(t, f.db, cmd)
	first := f.h.Run(ctx, cmd)
	if first.GetStatus() != typev1.Status_STATUS_OK {
		t.Fatalf("first start: %+v", first)
	}
	second := f.h.Run(ctx, cmd)
	if second.GetStatus() != typev1.Status_STATUS_OK {
		t.Fatalf("duplicate start: %+v", second)
	}
	if got := second.GetStartNativeSession(); got.GetProviderSessionId() != "provider-session-1" {
		t.Fatalf("duplicate start binding %+v", got)
	}
	if calls := f.fa.started(); len(calls) != 1 {
		t.Fatalf("adapter calls %v, want exactly one start", calls)
	}
}

// An ordinary turn waits for the earlier unresolved effect of its session
// in admission order; when the wait expires it gives up its own admission
// (NO_EFFECT) so the session stays live, and reports TURN_BUSY.
func TestHostSendWaitsForOrderThenGivesUpClean(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	startSession(t, f, startCmd("command-start"))

	// Block the first turn inside the adapter: the second send must wait.
	f.fa.blockSend = make(chan struct{})
	first := sendCmd("command-first", "session-1", 1, "one")
	loggedCommand(t, f.db, first)
	done := make(chan struct{})
	go func() {
		defer close(done)
		res := f.h.Run(ctx, first)
		if res.GetStatus() != typev1.Status_STATUS_OK {
			t.Errorf("blocked send: %+v", res)
		}
	}()

	second := sendCmd("command-second", "session-1", 1, "two")
	loggedCommand(t, f.db, second)
	res := f.h.Run(ctx, second)
	if res.GetReason() != typev1.ReasonCode_REASON_CODE_NATIVE_TURN_BUSY {
		t.Fatalf("overtaking send reason %v, want TURN_BUSY", res.GetReason())
	}
	if state := effectState(t, f, "command-second"); state != "no_effect" {
		t.Fatalf("overtaking effect state %s, want no_effect (own admission cleaned)", state)
	}
	if state := effectState(t, f, "command-first"); state != "may_have_run" {
		t.Fatalf("blocked effect state %s, want may_have_run", state)
	}

	// Release the first turn: it confirms, and a fresh third send proceeds.
	close(f.fa.blockSend)
	<-done
	third := sendCmd("command-third", "session-1", 1, "three")
	loggedCommand(t, f.db, third)
	if res := f.h.Run(ctx, third); res.GetStatus() != typev1.Status_STATUS_OK {
		t.Fatalf("third send: %+v", res)
	}
	// Every admitted effect consumes an order, including the abandoned
	// second: start 1, first 2, second 3, third 4.
	if got := res2order(t, f, "command-third"); got != 4 {
		t.Fatalf("third admission order %d, want 4", got)
	}
}

// An interrupt bypasses the admission order: it never waits behind the
// turn it stops, while an ordinary send behind the same blocker waits.
func TestHostInterruptBypassesOrder(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	startSession(t, f, startCmd("command-start"))

	f.fa.blockSend = make(chan struct{})
	first := sendCmd("command-first", "session-1", 1, "one")
	loggedCommand(t, f.db, first)
	done := make(chan struct{})
	go func() {
		defer close(done)
		f.h.Run(ctx, first)
	}()

	interrupt := interruptCmd("command-interrupt", "session-1", 1)
	loggedCommand(t, f.db, interrupt)
	res := f.h.Run(ctx, interrupt)
	if res.GetStatus() != typev1.Status_STATUS_OK || !res.GetInterruptNativeSession().GetAccepted() {
		t.Fatalf("interrupt behind a running turn: %+v", res)
	}
	if state := effectState(t, f, "command-interrupt"); state != "confirmed" {
		t.Fatalf("interrupt effect state %s, want confirmed", state)
	}
	close(f.fa.blockSend)
	<-done
}

// A stop bypasses the admission order like an interrupt.
func TestHostStopBypassesOrder(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	startSession(t, f, startCmd("command-start"))

	f.fa.blockSend = make(chan struct{})
	first := sendCmd("command-first", "session-1", 1, "one")
	loggedCommand(t, f.db, first)
	done := make(chan struct{})
	go func() {
		defer close(done)
		f.h.Run(ctx, first)
	}()

	stop := stopCmd("command-stop", "session-1", 1)
	loggedCommand(t, f.db, stop)
	res := f.h.Run(ctx, stop)
	if res.GetStatus() != typev1.Status_STATUS_OK {
		t.Fatalf("stop behind a running turn: %+v", res)
	}
	close(f.fa.blockSend)
	<-done
}

// An uncertain adapter outcome leaves the effect and answers
// UNKNOWN_OUTCOME, never a blind duplicate or a forged failure.
func TestHostUncertainCallHoldsForReconciliation(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.fa.sendErr = &native.CallError{Uncertain: true, Msg: "provider timed out"}
	ctx := context.Background()
	startSession(t, f, startCmd("command-start"))

	send := sendCmd("command-send", "session-1", 1, "maybe")
	loggedCommand(t, f.db, send)
	res := f.h.Run(ctx, send)
	if res.GetStatus() != typev1.Status_STATUS_UNKNOWN_OUTCOME {
		t.Fatalf("uncertain send status %v, want UNKNOWN_OUTCOME", res.GetStatus())
	}
	if res.GetRetryable() {
		t.Fatal("UNKNOWN_OUTCOME must never be retryable")
	}
	if state := effectState(t, f, "command-send"); state != "may_have_run" {
		t.Fatalf("uncertain effect state %s, want may_have_run", state)
	}
	if calls := f.fa.started(); len(calls) != 2 {
		t.Fatalf("adapter calls %v, want start + one send", calls)
	}
}

// A proven no-effect resolves NO_EFFECT and returns the adapter's reason:
// the hub retries with fresh ids, never by resending this one.
func TestHostProvenNoEffectReturnsAdapterReason(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.fa.sendErr = &native.CallError{
		Reason: typev1.ReasonCode_REASON_CODE_NATIVE_TURN_BUSY, Msg: "backend busy",
		Retryable: true,
	}
	ctx := context.Background()
	startSession(t, f, startCmd("command-start"))

	send := sendCmd("command-send", "session-1", 1, "later")
	loggedCommand(t, f.db, send)
	res := f.h.Run(ctx, send)
	if res.GetStatus() != typev1.Status_STATUS_FAILED || res.GetReason() != typev1.ReasonCode_REASON_CODE_NATIVE_TURN_BUSY {
		t.Fatalf("no-effect send: status=%v reason=%v", res.GetStatus(), res.GetReason())
	}
	if !res.GetRetryable() {
		t.Fatal("proven no-effect should be retryable with fresh ids")
	}
	if state := effectState(t, f, "command-send"); state != "no_effect" {
		t.Fatalf("effect state %s, want no_effect", state)
	}
}

func res2order(t *testing.T, f *fixture, id string) uint64 {
	t.Helper()
	e, err := f.db.ReadNativeEffect(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return e.AdmissionOrder
}
