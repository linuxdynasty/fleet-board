package ring_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/agent/ring"
)

func nativeFixture(t *testing.T) (*ring.DB, string, ring.NativeSessionRecord) {
	t.Helper()
	dir := stateDir(t)
	db := open(t, dir)
	state, err := db.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	s := nativeSession()
	s.InstallID = state.InstallID
	if _, err := db.CreateNativeSession(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	return db, dir, s
}

func admitNative(t *testing.T, db *ring.DB, session ring.NativeSessionRecord, effectID, commandID string, kind ring.NativeEffectKind) ring.NativeEffectRecord {
	t.Helper()
	body := []byte("encoded " + commandID)
	nativeCommandKind(t, db, commandID, "native."+string(kind), body)
	e, dup, err := db.AdmitNativeEffect(context.Background(), ring.NativeEffectRecord{
		EffectID: effectID, CommandID: commandID, SessionID: session.SessionID,
		Generation: session.Generation, Kind: kind, CommandBodyDigest: nativeDigest(body),
	})
	if err != nil || dup {
		t.Fatalf("admit %s: duplicate=%v err=%v", effectID, dup, err)
	}
	return e
}

func nativeCommandKind(t *testing.T, db *ring.DB, id, kind string, body []byte) {
	t.Helper()
	_, dup, err := db.LogCommand(context.Background(), ring.Command{ID: id, Kind: kind, Body: body})
	if err != nil || dup {
		t.Fatalf("log %s: duplicate=%v err=%v", id, dup, err)
	}
}

func TestNativeAdmissionBindsIdentityAndLoggedInput(t *testing.T) {
	db, _, session := nativeFixture(t)
	ctx := context.Background()
	e := admitNative(t, db, session, "effect-a", "command-a", ring.NativeEffectSendTurn)
	got, duplicate, err := db.AdmitNativeEffect(ctx, ring.NativeEffectRecord{
		EffectID: e.EffectID, CommandID: e.CommandID, SessionID: e.SessionID,
		Generation: e.Generation, Kind: e.Kind, CommandBodyDigest: e.CommandBodyDigest,
	})
	if err != nil || !duplicate || got.AdmissionOrder != 1 {
		t.Fatalf("duplicate: %+v duplicate=%v err=%v", got, duplicate, err)
	}
	for _, tc := range []struct {
		name   string
		change func(*ring.NativeEffectRecord)
	}{
		{"changed digest", func(x *ring.NativeEffectRecord) { x.CommandBodyDigest = nativeDigest([]byte("different")) }},
		{"other session", func(x *ring.NativeEffectRecord) { x.SessionID = "session-b" }},
		{"new effect same command", func(x *ring.NativeEffectRecord) { x.EffectID = "effect-b" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			attempt := ring.NativeEffectRecord{
				EffectID: e.EffectID, CommandID: e.CommandID, SessionID: e.SessionID,
				Generation: e.Generation, Kind: e.Kind, CommandBodyDigest: e.CommandBodyDigest,
			}
			tc.change(&attempt)
			if _, _, err := db.AdmitNativeEffect(ctx, attempt); !errors.Is(err, ring.ErrNativeConflict) {
				t.Fatalf("got %v, want ErrNativeConflict", err)
			}
		})
	}
	nativeCommandKind(t, db, "command-b", "native.send_turn", []byte("actual"))
	if _, _, err := db.AdmitNativeEffect(ctx, ring.NativeEffectRecord{
		EffectID: "effect-b", CommandID: "command-b", SessionID: session.SessionID,
		Generation: 1, Kind: ring.NativeEffectSendTurn, CommandBodyDigest: nativeDigest([]byte("altered")),
	}); !errors.Is(err, ring.ErrNativeConflict) {
		t.Fatalf("changed logged body: %v, want ErrNativeConflict", err)
	}
	if _, _, err := db.AdmitNativeEffect(ctx, ring.NativeEffectRecord{
		EffectID: "effect-c", CommandID: "missing", SessionID: session.SessionID,
		Generation: 1, Kind: ring.NativeEffectSendTurn, CommandBodyDigest: nativeDigest([]byte("missing")),
	}); !errors.Is(err, ring.ErrNativeConflict) {
		t.Fatalf("unlogged command: %v, want ErrNativeConflict", err)
	}
}

func TestNativeClaimChecksOwnerOrderAndOneWinner(t *testing.T) {
	db, _, session := nativeFixture(t)
	ctx := context.Background()
	first := admitNative(t, db, session, "effect-a", "command-a", ring.NativeEffectStart)
	second := admitNative(t, db, session, "effect-b", "command-b", ring.NativeEffectSendTurn)
	if second.AdmissionOrder != 2 {
		t.Fatalf("second order %d", second.AdmissionOrder)
	}
	if _, err := db.ClaimNativeEffect(ctx, first.EffectID, "other", 1); !errors.Is(err, ring.ErrNativeOwner) {
		t.Fatalf("wrong owner: %v", err)
	}
	if _, err := db.ClaimNativeEffect(ctx, first.EffectID, session.ControllerID, 2); !errors.Is(err, ring.ErrNativeOwner) {
		t.Fatalf("stale generation: %v", err)
	}
	if _, err := db.ClaimNativeEffect(ctx, second.EffectID, session.ControllerID, 1); !errors.Is(err, ring.ErrNativeOrder) {
		t.Fatalf("second before first: %v", err)
	}
	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := db.ClaimNativeEffect(ctx, first.EffectID, session.ControllerID, 1)
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	var success, already int
	for err := range results {
		switch {
		case err == nil:
			success++
		case errors.Is(err, ring.ErrNativeClaimed):
			already++
		default:
			t.Fatalf("claim: %v", err)
		}
	}
	if success != 1 || already != 1 {
		t.Fatalf("claim winners=%d already=%d, want 1 each", success, already)
	}
	if _, err := db.ClaimNativeEffect(ctx, second.EffectID, session.ControllerID, 1); !errors.Is(err, ring.ErrNativeOrder) {
		t.Fatalf("second while first uncertain: %v", err)
	}
}

func TestNativeConcurrentDuplicateAdmissionHasOneOrder(t *testing.T) {
	db, _, session := nativeFixture(t)
	ctx := context.Background()
	body := []byte("one input")
	nativeCommandKind(t, db, "command-a", "native.start", body)
	input := ring.NativeEffectRecord{
		EffectID: "effect-a", CommandID: "command-a",
		SessionID: session.SessionID, Generation: 1, Kind: ring.NativeEffectStart,
		CommandBodyDigest: nativeDigest(body),
	}
	start := make(chan struct{})
	type result struct {
		entry ring.NativeEffectRecord
		dup   bool
		err   error
	}
	results := make(chan result, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			e, dup, err := db.AdmitNativeEffect(ctx, input)
			results <- result{e, dup, err}
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	var inserted, duplicate int
	for r := range results {
		if r.err != nil || r.entry.AdmissionOrder != 1 {
			t.Fatalf("admission: %+v", r)
		}
		if r.dup {
			duplicate++
		} else {
			inserted++
		}
	}
	if inserted != 1 || duplicate != 1 {
		t.Fatalf("new=%d duplicate=%d, want one each", inserted, duplicate)
	}
	list, err := db.ListUnresolvedNativeEffects(ctx, "", 10)
	if err != nil || len(list) != 1 {
		t.Fatalf("journal rows: %+v err=%v", list, err)
	}
}
