package native_test

import (
	"context"
	"testing"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/ring"
)

// Crash before spawn: the command was logged and its effect admitted, but
// the claim never committed. After reopen, recovery resolves the dead
// attempt NO_EFFECT, the rerun reconciles UNKNOWN_OUTCOME without calling,
// and the session stays live for a fresh turn.
func TestHostCrashBeforeSpawnNeverDuplicates(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	startSession(t, f, startCmd("command-start"))

	send := sendCmd("command-send", "session-1", 1, "do it")
	digest := loggedCommand(t, f.db, send)
	if _, _, err := f.db.AdmitNativeEffect(ctx, ring.NativeEffectRecord{
		EffectID: "command-send", CommandID: "command-send", SessionID: "session-1",
		Generation: 1, Kind: ring.NativeEffectSendTurn, CommandBodyDigest: digest,
	}); err != nil {
		t.Fatal(err)
	}

	f.reopen(t)
	if err := f.h.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	if state := effectState(t, f, "command-send"); state != "no_effect" {
		t.Fatalf("dead attempt state %s, want no_effect", state)
	}

	res := f.h.Run(ctx, send)
	if res.GetStatus() != typev1.Status_STATUS_UNKNOWN_OUTCOME {
		t.Fatalf("rerun status %v, want UNKNOWN_OUTCOME", res.GetStatus())
	}
	if calls := f.fa.started(); len(calls) != 1 {
		t.Fatalf("adapter calls %v, want only the start: no duplicate turn", calls)
	}

	fresh := sendCmd("command-fresh", "session-1", 1, "again")
	loggedCommand(t, f.db, fresh)
	if res := f.h.Run(ctx, fresh); res.GetStatus() != typev1.Status_STATUS_OK {
		t.Fatalf("fresh send after recovery: %+v", res)
	}
}

// Crash after spawn: the claim committed but no result was recorded. After
// reopen the effect is RECONCILE_REQUIRED and the session held; the rerun
// answers UNKNOWN_OUTCOME, an ordinary send is HELD, and no second call
// runs until reconciliation.
func TestHostCrashAfterSpawnHoldsSession(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	startSession(t, f, startCmd("command-start"))

	send := sendCmd("command-send", "session-1", 1, "maybe ran")
	digest := loggedCommand(t, f.db, send)
	effect, _, err := f.db.AdmitNativeEffect(ctx, ring.NativeEffectRecord{
		EffectID: "command-send", CommandID: "command-send", SessionID: "session-1",
		Generation: 1, Kind: ring.NativeEffectSendTurn, CommandBodyDigest: digest,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.ClaimNativeEffect(ctx, effect.EffectID, "machine-1", 1); err != nil {
		t.Fatal(err)
	}

	f.reopen(t)
	if err := f.h.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	if state := effectState(t, f, "command-send"); state != "reconcile_required" {
		t.Fatalf("crashed effect state %s, want reconcile_required", state)
	}

	res := f.h.Run(ctx, send)
	if res.GetReason() != typev1.ReasonCode_REASON_CODE_NATIVE_SESSION_HELD {
		t.Fatalf("rerun reason %v, want SESSION_HELD: the held session names its reconciliation", res.GetReason())
	}

	next := sendCmd("command-next", "session-1", 1, "later")
	loggedCommand(t, f.db, next)
	res = f.h.Run(ctx, next)
	if res.GetReason() != typev1.ReasonCode_REASON_CODE_NATIVE_SESSION_HELD {
		t.Fatalf("send on held session reason %v, want SESSION_HELD", res.GetReason())
	}
	// The refusal happens before admission: no second row, nothing to clean.
	if _, err := f.db.ReadNativeEffect(ctx, "command-next"); err == nil {
		t.Fatal("refused send admitted an effect on a held session")
	}
	if calls := f.fa.started(); len(calls) != 1 {
		t.Fatalf("adapter calls %v, want only the start: nothing reran", calls)
	}
}

// A clean restart recovers nothing, holds nothing and leaves the session
// live: recovery is a no-op without uncertainty.
func TestHostCleanRestartRecoversNothing(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	startSession(t, f, startCmd("command-start"))

	f.reopen(t)
	if err := f.h.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	if err := f.h.Recover(ctx); err != nil {
		t.Fatalf("second recovery: %v", err)
	}

	send := sendCmd("command-send", "session-1", 1, "still live")
	loggedCommand(t, f.db, send)
	if res := f.h.Run(ctx, send); res.GetStatus() != typev1.Status_STATUS_OK {
		t.Fatalf("send after clean restart: %+v", res)
	}
}
