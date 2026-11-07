package ring_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/agent/ring"
	"github.com/linuxdynasty/fleet-board/internal/store"
)

// The wire-kind guard (migration 10) is the RED side of the #266 contract
// slice: the old 'native.<kind>' spelling of the logged command no longer
// admits an effect, and the executor's spelling (the Command.kind arm's
// message name) does.
func TestNativeWireKindsReplaceTheOldGuard(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, dir, session := nativeFixture(t)
	body := []byte("encoded wire command")
	nativeCommandKind(t, db, "command-wire", ring.NativeWireKind(ring.NativeEffectStart), body)
	e, dup, err := db.AdmitNativeEffect(ctx, ring.NativeEffectRecord{
		EffectID: "effect-wire", CommandID: "command-wire", SessionID: session.SessionID,
		Generation: session.Generation, Kind: ring.NativeEffectStart, CommandBodyDigest: nativeDigest(body),
	})
	if err != nil || dup {
		t.Fatalf("wire spelling: duplicate=%v err=%v", dup, err)
	}
	_ = e

	old := []byte("encoded old command")
	nativeCommandKind(t, db, "command-old", "native.start", old)
	if _, _, err := db.AdmitNativeEffect(ctx, ring.NativeEffectRecord{
		EffectID: "effect-old", CommandID: "command-old", SessionID: session.SessionID,
		Generation: session.Generation, Kind: ring.NativeEffectStart, CommandBodyDigest: nativeDigest(old),
	}); err == nil {
		t.Fatal("old 'native.<kind>' spelling was admitted")
	}

	f := raw(t, dir)
	var trigger string
	if err := f.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE name = 'native_effect_logged'`).Scan(&trigger); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"StartNativeSession", "SendNativeTurn", "ResumeNativeSession", "InterruptNativeSession", "StopNativeSession"} {
		if !strings.Contains(trigger, want) {
			t.Errorf("guard names no %s", want)
		}
	}
	if strings.Contains(trigger, "'native.'") {
		t.Error("guard still uses the old 'native.' prefix")
	}
}

// A file that stopped at schema 9 migrates to 10 and admits under the wire
// spelling; the old spelling stays refused there too.
func TestNativeWireKindsMigrateFromSchemaNine(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := stateDir(t)
	if err := os.WriteFile(ring.Path(dir), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	old, err := store.Open(ctx, store.Options{Path: ring.Path(dir), Name: "agent", Migrations: ring.Migrations()[:9], MustExist: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	db := open(t, dir)
	if got := db.SchemaVersion(); got != len(ring.Migrations()) || got != 10 {
		t.Fatalf("schema version %d, want 10", got)
	}
	st, err := db.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	s := nativeSession()
	s.InstallID = st.InstallID
	if _, err := db.CreateNativeSession(ctx, s); err != nil {
		t.Fatal(err)
	}
	body := []byte("encoded migrated command")
	nativeCommandKind(t, db, "command-m", "SendNativeTurn", body)
	if _, _, err := db.AdmitNativeEffect(ctx, ring.NativeEffectRecord{
		EffectID: "effect-m", CommandID: "command-m", SessionID: s.SessionID,
		Generation: s.Generation, Kind: ring.NativeEffectSendTurn, CommandBodyDigest: nativeDigest(body),
	}); err != nil {
		t.Fatalf("wire spelling after migrate: %v", err)
	}
	var applied int
	f := raw(t, dir)
	if err := f.QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if applied != 10 {
		t.Errorf("schema_migrations holds %d rows, want 10", applied)
	}
}
