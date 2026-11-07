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

// Crash between the provider bind and the resolve: the session is bound
// but the start effect never confirmed. This is the only crash window
// start keeps: the bind commits before the resolve, so a confirmed effect
// always has its provider id. The setup below builds that window with the
// journal primitives (no seam can stop a production start between two
// adjacent commits), then locks the recovery contract: the rerun must not
// call the adapter again, the binding must survive, and a fresh start on
// the same session must wait instead of spawning a second provider.
func TestHostCrashBetweenBindAndResolveKeepsBinding(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	start := startCmd("command-start")
	digest := loggedCommand(t, f.db, start)
	st, err := f.db.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.CreateNativeSession(ctx, ring.NativeSessionRecord{
		SessionID: "session-1", MachineID: "machine-1", InstallID: st.InstallID,
		Harness: "claude_code", Interface: "default",
		AccountRef: "account-1", ProfileRef: "default",
		WorkdirRef: "wrk-0123456789abcdef01234567", Model: "opus", Effort: "high",
		QualificationRef: "native.claude_code/1_cli-9_plan",
		ControllerID:     "machine-1", Generation: 1, ControlState: ring.NativeControlManaged,
	}); err != nil {
		t.Fatal(err)
	}
	effect, dup, err := f.db.AdmitNativeEffect(ctx, ring.NativeEffectRecord{
		EffectID: "command-start", CommandID: "command-start", SessionID: "session-1",
		Generation: 1, Kind: ring.NativeEffectStart, CommandBodyDigest: digest,
	})
	if err != nil || dup {
		t.Fatalf("admit: dup=%v err=%v", dup, err)
	}
	if _, err := f.db.ClaimNativeEffect(ctx, effect.EffectID, "machine-1", 1); err != nil {
		t.Fatal(err)
	}
	// The bind committed; the resolve never ran.
	if _, err := f.db.BindNativeProviderSession(ctx, "session-1", "machine-1", 1,
		"command-start", "provider-session-1"); err != nil {
		t.Fatal(err)
	}

	f.reopen(t)
	if err := f.h.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	if state := effectState(t, f, "command-start"); state != "reconcile_required" {
		t.Fatalf("crashed effect state %s, want reconcile_required", state)
	}
	res := f.h.Run(ctx, start)
	if res.GetStatus() != typev1.Status_STATUS_UNKNOWN_OUTCOME {
		t.Fatalf("rerun status %v, want UNKNOWN_OUTCOME: no second provider call", res.GetStatus())
	}
	if calls := f.fa.started(); len(calls) != 0 {
		t.Fatalf("adapter calls %v, want none: the bound start must not respawn", calls)
	}
	session, err := f.db.ReadNativeSession(ctx, "session-1")
	if err != nil {
		t.Fatal(err)
	}
	if session.ProviderSessionID != "provider-session-1" {
		t.Fatalf("bound provider %q, want provider-session-1", session.ProviderSessionID)
	}
	fresh := startCmd("command-fresh")
	loggedCommand(t, f.db, fresh)
	res = f.h.Run(ctx, fresh)
	if res.GetReason() != typev1.ReasonCode_REASON_CODE_NATIVE_SESSION_HELD {
		t.Fatalf("fresh start reason %v, want SESSION_HELD: the held session waits", res.GetReason())
	}
	if calls := f.fa.started(); len(calls) != 0 {
		t.Fatalf("adapter calls %v, want none after the held refusal", calls)
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
