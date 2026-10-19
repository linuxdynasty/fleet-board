package ring_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/ring"
)

func nativeDigest(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func nativeSession() ring.NativeSessionRecord {
	return ring.NativeSessionRecord{
		SessionID: "session-a", MachineID: "machine-a", InstallID: "install-a",
		Harness: "codex", Interface: "app-server", AccountRef: "account-a",
		ProfileRef: "profile-a", WorkdirRef: "repo-a", Model: "model-a", Effort: "high",
		QualificationRef: "qual-a", ControllerID: "controller-a", Generation: 1,
		ControlState: ring.NativeControlManaged,
	}
}

func nativeCommand(t *testing.T, db *ring.DB, id string, body []byte) {
	t.Helper()
	_, duplicate, err := db.LogCommand(context.Background(), ring.Command{
		ID: id, Kind: "native.send_turn", Epoch: 1, Seq: 1,
		Received: time.Unix(100, 0), NotAfter: time.Unix(200, 0), Body: body,
	})
	if err != nil || duplicate {
		t.Fatalf("LogCommand: duplicate=%v err=%v", duplicate, err)
	}
}

func TestNativeJournalClaimIsDurableAndSingleUse(t *testing.T) {
	dir := stateDir(t)
	ctx := context.Background()
	db := open(t, dir)
	session := nativeSession()
	state, err := db.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	session.InstallID = state.InstallID
	if _, err := db.CreateNativeSession(ctx, session); err != nil {
		t.Fatal(err)
	}
	input := []byte("encoded command one")
	nativeCommand(t, db, "command-a", input)
	effect := ring.NativeEffectRecord{
		EffectID: "effect-a", CommandID: "command-a", SessionID: session.SessionID,
		Generation: session.Generation, Kind: ring.NativeEffectSendTurn,
		CommandBodyDigest: nativeDigest(input),
	}
	got, duplicate, err := db.AdmitNativeEffect(ctx, effect)
	if err != nil || duplicate || got.State != ring.NativeEffectPrepared || got.AdmissionOrder != 1 {
		t.Fatalf("admit: %+v duplicate=%v err=%v", got, duplicate, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db = open(t, dir)
	claimed, err := db.ClaimNativeEffect(ctx, effect.EffectID, session.ControllerID, session.Generation)
	if err != nil || claimed.State != ring.NativeEffectMayHaveRun {
		t.Fatalf("claim: %+v err=%v", claimed, err)
	}
	if _, err := db.ClaimNativeEffect(ctx, effect.EffectID, session.ControllerID, session.Generation); !errors.Is(err, ring.ErrNativeClaimed) {
		t.Fatalf("second claim: %v, want ErrNativeClaimed", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db = open(t, dir)
	work, err := db.RecoverNativeEffects(ctx, "", 10)
	if err != nil || len(work) != 1 || work[0].State != ring.NativeEffectReconcileRequired {
		t.Fatalf("recover: %+v err=%v", work, err)
	}
	if _, err := db.ClaimNativeEffect(ctx, effect.EffectID, session.ControllerID, session.Generation); !errors.Is(err, ring.ErrNativeClaimed) {
		t.Fatalf("claim after recovery: %v, want ErrNativeClaimed", err)
	}
}
