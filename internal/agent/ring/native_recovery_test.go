package ring_test

import (
	"context"
	"errors"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/agent/ring"
)

func TestNativePreparedRestartAndUncertainExecution(t *testing.T) {
	db, dir, session := nativeFixture(t)
	ctx := context.Background()
	e := admitNative(t, db, session, "effect-a", "command-a", ring.NativeEffectStart)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db = open(t, dir)
	prepared, err := db.RecoverNativeEffects(ctx, "", 10)
	if err != nil || len(prepared) != 1 || prepared[0].State != ring.NativeEffectPrepared {
		t.Fatalf("restart before claim: %+v err=%v", prepared, err)
	}
	if _, err := db.ClaimNativeEffect(ctx, e.EffectID, session.ControllerID, 1); err != nil {
		t.Fatal(err)
	}
	externalCalls := 1 // synthetic sole controller in this test calls after the committed claim
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db = open(t, dir)
	uncertain, err := db.RecoverNativeEffects(ctx, "", 10)
	if err != nil || len(uncertain) != 1 || uncertain[0].State != ring.NativeEffectReconcileRequired {
		t.Fatalf("restart after simulated call: %+v err=%v", uncertain, err)
	}
	if _, err := db.ClaimNativeEffect(ctx, e.EffectID, session.ControllerID, 1); !errors.Is(err, ring.ErrNativeClaimed) {
		t.Fatalf("duplicate after lost response: %v", err)
	}
	if externalCalls != 1 {
		t.Fatalf("external calls %d, want one", externalCalls)
	}
	held, err := db.ReadNativeSession(ctx, session.SessionID)
	if err != nil || held.ControlState != ring.NativeControlHeld {
		t.Fatalf("held owner: %+v err=%v", held, err)
	}
	resolved, err := db.ResolveNativeEffect(ctx, e.EffectID, session.ControllerID, 1,
		ring.NativeEffectConfirmed, ring.NativeEvidence{
			Kind:   ring.NativeEvidenceProviderReceipt,
			Digest: nativeDigest([]byte("provider receipt")),
		})
	if err != nil || resolved.State != ring.NativeEffectConfirmed {
		t.Fatalf("resolve: %+v err=%v", resolved, err)
	}
	if unresolved, err := db.ListUnresolvedNativeEffects(ctx, "", 10); err != nil || len(unresolved) != 0 {
		t.Fatalf("unresolved: %+v err=%v", unresolved, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db = open(t, dir)
	if _, err := db.ClaimNativeEffect(ctx, e.EffectID, session.ControllerID, 1); !errors.Is(err, ring.ErrNativeClaimed) {
		t.Fatalf("claim after resolved restart: %v", err)
	}
}

func TestNativeHeldSessionAllowsInterruptButBlocksNewWork(t *testing.T) {
	db, _, session := nativeFixture(t)
	ctx := context.Background()
	first := admitNative(t, db, session, "effect-a", "command-a", ring.NativeEffectSendTurn)
	if _, err := db.ClaimNativeEffect(ctx, first.EffectID, session.ControllerID, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RecoverNativeEffects(ctx, "", 10); err != nil {
		t.Fatal(err)
	}
	body := []byte("held work")
	nativeCommandKind(t, db, "command-b", "native.resume", body)
	if _, _, err := db.AdmitNativeEffect(ctx, ring.NativeEffectRecord{
		EffectID: "effect-b", CommandID: "command-b", SessionID: session.SessionID,
		Generation: 1, Kind: ring.NativeEffectResume, CommandBodyDigest: nativeDigest(body),
	}); !errors.Is(err, ring.ErrNativeHeld) {
		t.Fatalf("ordinary work on held session: %v", err)
	}
	urgent := admitNative(t, db, session, "effect-c", "command-c", ring.NativeEffectInterrupt)
	if _, err := db.ClaimNativeEffect(ctx, urgent.EffectID, session.ControllerID, 1); err != nil {
		t.Fatalf("interrupt held session: %v", err)
	}
}

func TestNativeUnresolvedPagesAndBounds(t *testing.T) {
	db, _, session := nativeFixture(t)
	ctx := context.Background()
	for _, id := range []string{"a", "b", "c"} {
		admitNative(t, db, session, "effect-"+id, "command-"+id, ring.NativeEffectStart)
	}
	page, err := db.ListUnresolvedNativeEffects(ctx, "", 2)
	if err != nil || len(page) != 2 || page[0].EffectID != "effect-a" || page[1].EffectID != "effect-b" {
		t.Fatalf("first page: %+v err=%v", page, err)
	}
	page, err = db.ListUnresolvedNativeEffects(ctx, page[1].EffectID, 2)
	if err != nil || len(page) != 1 || page[0].EffectID != "effect-c" {
		t.Fatalf("next page: %+v err=%v", page, err)
	}
	for _, limit := range []int{0, 1001} {
		if _, err := db.ListUnresolvedNativeEffects(ctx, "", limit); !errors.Is(err, ring.ErrNativeBadInput) {
			t.Fatalf("list limit %d: %v", limit, err)
		}
		if _, err := db.RecoverNativeEffects(ctx, "", limit); !errors.Is(err, ring.ErrNativeBadInput) {
			t.Fatalf("recover limit %d: %v", limit, err)
		}
	}
}
