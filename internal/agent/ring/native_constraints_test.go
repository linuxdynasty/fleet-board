package ring_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/ring"
	"github.com/linuxdynasty/fleet-board/internal/store"
)

func TestNativeJournalSurvivesCommandPruningAndKeepsTerminalCounts(t *testing.T) {
	db, dir, session := nativeFixture(t)
	ctx := context.Background()
	e := admitNative(t, db, session, "effect-a", "command-a", ring.NativeEffectStart)
	if err := db.RecordResult(ctx, e.CommandID, []byte("terminal result"), time.Unix(300, 0)); err != nil {
		t.Fatal(err)
	}
	if db.UnackedCount() != 1 {
		t.Fatalf("unacked count %d, want one", db.UnackedCount())
	}
	n, err := db.Prune(ctx, time.Unix(10*24*3600, 0))
	if err != nil || n != 1 || db.UnackedCount() != 0 {
		t.Fatalf("prune n=%d unacked=%d err=%v", n, db.UnackedCount(), err)
	}
	if _, err := db.Command(ctx, e.CommandID); !errors.Is(err, ring.ErrUnknownCommand) {
		t.Fatalf("pruned command: %v", err)
	}
	got, dup, err := db.AdmitNativeEffect(ctx, ring.NativeEffectRecord{
		EffectID: e.EffectID, CommandID: e.CommandID, SessionID: e.SessionID,
		Generation: e.Generation, Kind: e.Kind, CommandBodyDigest: e.CommandBodyDigest,
	})
	if err != nil || !dup || got.State != ring.NativeEffectPrepared {
		t.Fatalf("effect after command prune: %+v dup=%v err=%v", got, dup, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db = open(t, dir)
	if got, err := db.ListUnresolvedNativeEffects(ctx, "", 10); err != nil || len(got) != 1 {
		t.Fatalf("journal after reopen: %+v err=%v", got, err)
	}
}

func TestNativeSchemaGuardsIdentityAndStateAgainstDirectSQL(t *testing.T) {
	db, dir, session := nativeFixture(t)
	ctx := context.Background()
	e := admitNative(t, db, session, "effect-a", "command-a", ring.NativeEffectStart)
	file := raw(t, dir)
	nativeCommandKind(t, db, "command-b", "native.start", []byte("other input"))
	for _, statement := range []string{
		`UPDATE native_sessions SET generation = 2 WHERE session_id = 'session-a'`,
		`UPDATE native_sessions SET controller_id = 'other' WHERE session_id = 'session-a'`,
		`UPDATE native_sessions SET provider_session_id = 'manual', start_effect_id = 'effect-a' WHERE session_id = 'session-a'`,
		`DELETE FROM native_sessions WHERE session_id = 'session-a'`,
		`INSERT OR REPLACE INTO native_sessions SELECT * FROM native_sessions WHERE session_id = 'session-a'`,
		`UPDATE native_effects SET command_body_digest = 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa' WHERE effect_id = 'effect-a'`,
		`UPDATE native_effects SET admission_order = 2 WHERE effect_id = 'effect-a'`,
		`UPDATE native_effects SET state = 'confirmed' WHERE effect_id = 'effect-a'`,
		`DELETE FROM native_effects WHERE effect_id = 'effect-a'`,
		`INSERT OR REPLACE INTO native_effects SELECT * FROM native_effects WHERE effect_id = 'effect-a'`,
		`INSERT INTO native_effects(effect_id, command_id, session_id, generation, kind, command_body_digest, admission_order, state)
			VALUES ('effect-b', 'command-b', 'session-a', 2, 'start', 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', 2, 'prepared')`,
		`INSERT INTO native_effects(effect_id, command_id, session_id, generation, kind, command_body_digest, admission_order, state)
			VALUES ('effect-b', 'missing-command', 'session-a', 1, 'start', 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', 2, 'prepared')`,
		`INSERT INTO native_effects(effect_id, command_id, session_id, generation, kind, command_body_digest, admission_order, state)
			VALUES ('effect-b', 'command-b', 'session-a', 1, 'start', 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', 5, 'prepared')`,
		`UPDATE native_effects SET state = 'may_have_run', evidence_kind = 'provider_receipt' WHERE effect_id = 'effect-a'`,
	} {
		if _, err := file.ExecContext(ctx, statement); err == nil {
			t.Errorf("direct SQL unexpectedly succeeded: %s", statement)
		}
	}
	stored, err := db.ReadNativeSession(ctx, session.SessionID)
	if err != nil || stored.Generation != 1 || stored.ControllerID != session.ControllerID {
		t.Fatalf("session changed: %+v err=%v", stored, err)
	}
	list, err := db.ListUnresolvedNativeEffects(ctx, "", 10)
	if err != nil || len(list) != 1 || list[0].CommandBodyDigest != e.CommandBodyDigest || list[0].State != ring.NativeEffectPrepared {
		t.Fatalf("effect changed: %+v err=%v", list, err)
	}
}

func TestNativeProviderIdentityUsesCompleteLocalNamespace(t *testing.T) {
	db, _, first := nativeFixture(t)
	ctx := context.Background()
	start := admitNative(t, db, first, "effect-a", "command-a", ring.NativeEffectStart)
	if _, err := db.BindNativeProviderSession(ctx, first.SessionID, first.ControllerID, 1, start.EffectID, "provider-1"); !errors.Is(err, ring.ErrNativeConflict) {
		t.Fatalf("binding without a claimed start: %v", err)
	}
	if _, err := db.ClaimNativeEffect(ctx, start.EffectID, first.ControllerID, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := db.BindNativeProviderSession(ctx, first.SessionID, first.ControllerID, 1, start.EffectID, "provider-1"); err != nil {
		t.Fatal(err)
	}
	second := first
	second.SessionID = "session-b"
	if _, err := db.CreateNativeSession(ctx, second); err != nil {
		t.Fatal(err)
	}
	secondStart := admitNative(t, db, second, "effect-b", "command-b", ring.NativeEffectStart)
	if _, err := db.ClaimNativeEffect(ctx, secondStart.EffectID, second.ControllerID, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := db.BindNativeProviderSession(ctx, second.SessionID, second.ControllerID, 1, secondStart.EffectID, "provider-1"); !errors.Is(err, ring.ErrNativeConflict) {
		t.Fatalf("same namespace/provider id: %v", err)
	}
	second.SessionID = "session-c"
	second.ProfileRef = "profile-b"
	if _, err := db.CreateNativeSession(ctx, second); err != nil {
		t.Fatalf("different profile: %v", err)
	}
	thirdStart := admitNative(t, db, second, "effect-c", "command-c", ring.NativeEffectStart)
	if _, err := db.ClaimNativeEffect(ctx, thirdStart.EffectID, second.ControllerID, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := db.BindNativeProviderSession(ctx, second.SessionID, second.ControllerID, 1, thirdStart.EffectID, "provider-1"); err != nil {
		t.Fatalf("different profile binding: %v", err)
	}
	if _, err := db.BindNativeProviderSession(ctx, first.SessionID, first.ControllerID, 1, start.EffectID, "changed"); !errors.Is(err, ring.ErrNativeConflict) {
		t.Fatalf("provider identity rewrite: %v", err)
	}
}

func TestNativeMetadataBoundsAndPreviousSchemaMigration(t *testing.T) {
	db, _, session := nativeFixture(t)
	ctx := context.Background()
	for _, tc := range []struct {
		name, value string
	}{
		{"oversized model", strings.Repeat("x", 129)},
		{"control character", "x\nsecret"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := session
			bad.SessionID = "bad-" + strings.ReplaceAll(tc.name, " ", "-")
			bad.Model = tc.value
			if _, err := db.CreateNativeSession(ctx, bad); !errors.Is(err, ring.ErrNativeBadInput) {
				t.Fatalf("bad metadata: %v", err)
			}
		})
	}
	dir := stateDir(t)
	if err := os.WriteFile(ring.Path(dir), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	previous, err := store.Open(ctx, store.Options{
		Path: ring.Path(dir), Name: "agent",
		Migrations: ring.Migrations()[:8], MustExist: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := previous.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO agent_state(id, install_id, created_time)
			VALUES (1, 'prior-install', 0)`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := previous.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded := open(t, dir)
	if got := upgraded.SchemaVersion(); got != 9 {
		t.Fatalf("schema version %d, want 9", got)
	}
	state, err := upgraded.State(ctx)
	if err != nil || state.InstallID != "prior-install" {
		t.Fatalf("kept old install: %+v err=%v", state, err)
	}
	if err := upgraded.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded = open(t, dir)
	if got := upgraded.SchemaVersion(); got != 9 {
		t.Fatalf("reopen schema version %d, want 9", got)
	}
}

func TestNativeDirectSQLCannotClaimOutOfOrderOrWhileHeld(t *testing.T) {
	db, dir, session := nativeFixture(t)
	ctx := context.Background()
	first := admitNative(t, db, session, "effect-a", "command-a", ring.NativeEffectStart)
	second := admitNative(t, db, session, "effect-b", "command-b", ring.NativeEffectSendTurn)
	file := raw(t, dir)
	if _, err := file.ExecContext(ctx, `UPDATE native_effects SET state = 'may_have_run'
		WHERE effect_id = ?`, second.EffectID); err == nil {
		t.Fatal("direct SQL claimed work ahead of its predecessor")
	}
	if _, err := file.ExecContext(ctx, `UPDATE native_sessions SET control_state = 'held'
		WHERE session_id = ?`, session.SessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := file.ExecContext(ctx, `UPDATE native_effects SET state = 'may_have_run'
		WHERE effect_id = ?`, first.EffectID); err == nil {
		t.Fatal("direct SQL claimed ordinary work while held")
	}
}
