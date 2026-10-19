package ring_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/ring"
)

func TestNativeFailedAndUncertainClaimReturnNoReceipt(t *testing.T) {
	db, dir, session := nativeFixture(t)
	ctx := context.Background()
	effect := admitNative(t, db, session, "effect-a", "command-a", ring.NativeEffectStart)
	file := raw(t, dir)
	if _, err := file.ExecContext(ctx, `CREATE TRIGGER native_test_abort BEFORE UPDATE OF state ON native_effects
		WHEN NEW.state = 'may_have_run' BEGIN SELECT RAISE(ABORT, 'simulated disk refusal'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ClaimNativeEffect(ctx, effect.EffectID, session.ControllerID, 1); err == nil {
		t.Fatal("failed claim returned a receipt")
	}
	stored, err := db.ReadNativeEffect(ctx, effect.EffectID)
	if err != nil || stored.State != ring.NativeEffectPrepared {
		t.Fatalf("failed claim changed row: %+v err=%v", stored, err)
	}
	if _, err := file.ExecContext(ctx, `DROP TRIGGER native_test_abort`); err != nil {
		t.Fatal(err)
	}
	short, cancel := context.WithTimeout(ctx, time.Millisecond)
	defer cancel()
	_, err = db.ClaimNativeEffect(short, effect.EffectID, session.ControllerID, 1)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("uncertain claim returned %v, want deadline with no receipt", err)
	}
	// A later writer job is a barrier: if the timed-out claim was queued, its
	// outcome has committed or rolled back before this read.
	nativeCommandKind(t, db, "barrier-command", "unrelated", []byte("barrier"))
	stored, err = db.ReadNativeEffect(ctx, effect.EffectID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.State != ring.NativeEffectPrepared && stored.State != ring.NativeEffectMayHaveRun {
		t.Fatalf("uncertain claim has impossible state %s", stored.State)
	}
	// The caller of the timed-out claim cannot infer dispatch authority from
	// this read. If it committed, a second claim is refused.
	if stored.State == ring.NativeEffectMayHaveRun {
		if _, err := db.ClaimNativeEffect(ctx, effect.EffectID, session.ControllerID, 1); !errors.Is(err, ring.ErrNativeClaimed) {
			t.Fatalf("second claim after uncertain commit: %v", err)
		}
	}
}
