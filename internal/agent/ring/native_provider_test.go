package ring_test

import (
	"context"
	"errors"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/agent/ring"
)

func TestNativeProviderBindingNamesOneClaimedStartAndSurvivesReopen(t *testing.T) {
	db, dir, session := nativeFixture(t)
	ctx := context.Background()
	start := admitNative(t, db, session, "effect-a", "command-a", ring.NativeEffectStart)
	unclaimed := admitNative(t, db, session, "effect-b", "command-b", ring.NativeEffectStart)
	if _, err := db.ClaimNativeEffect(ctx, start.EffectID, session.ControllerID, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := db.BindNativeProviderSession(ctx, session.SessionID, session.ControllerID, 1,
		unclaimed.EffectID, "provider-a"); !errors.Is(err, ring.ErrNativeConflict) {
		t.Fatalf("unclaimed start bound: %v", err)
	}
	if _, err := db.BindNativeProviderSession(ctx, session.SessionID, session.ControllerID, 1,
		"effect-missing", "provider-a"); !errors.Is(err, ring.ErrNativeConflict) {
		t.Fatalf("missing start bound: %v", err)
	}
	bound, err := db.BindNativeProviderSession(ctx, session.SessionID, session.ControllerID, 1,
		start.EffectID, "provider-a")
	if err != nil || bound.ProviderSessionID != "provider-a" || bound.StartEffectID != start.EffectID {
		t.Fatalf("bound pair: %+v err=%v", bound, err)
	}
	again, err := db.BindNativeProviderSession(ctx, session.SessionID, session.ControllerID, 1,
		start.EffectID, "provider-a")
	if err != nil || again != bound {
		t.Fatalf("same pair replay: %+v err=%v", again, err)
	}
	for _, pair := range [][2]string{{unclaimed.EffectID, "provider-a"}, {start.EffectID, "provider-b"}} {
		if _, err := db.BindNativeProviderSession(ctx, session.SessionID, session.ControllerID, 1,
			pair[0], pair[1]); !errors.Is(err, ring.ErrNativeConflict) {
			t.Fatalf("rebind %+v: %v", pair, err)
		}
	}
	if _, err := db.ResolveNativeEffect(ctx, start.EffectID, session.ControllerID, 1,
		ring.NativeEffectNoEffect, noEffectEvidence()); !errors.Is(err, ring.ErrNativeConflict) {
		t.Fatalf("bound start resolved NO_EFFECT: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db = open(t, dir)
	stored, err := db.ReadNativeSession(ctx, session.SessionID)
	if err != nil || stored.ProviderSessionID != "provider-a" || stored.StartEffectID != start.EffectID {
		t.Fatalf("reopened pair: %+v err=%v", stored, err)
	}
	file := raw(t, dir)
	if _, err := file.ExecContext(ctx, `UPDATE native_effects SET state = 'no_effect',
		evidence_kind = 'prelaunch_refusal',
		evidence_digest = 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'
		WHERE effect_id = 'effect-a'`); err == nil {
		t.Fatal("direct SQL discarded a bound start")
	}
}

func TestNativePreboundInsertIsRefused(t *testing.T) {
	db, dir, session := nativeFixture(t)
	ctx := context.Background()
	bound := session
	bound.SessionID = "session-b"
	bound.ProviderSessionID = "provider-a"
	bound.StartEffectID = "effect-a"
	if _, err := db.CreateNativeSession(ctx, bound); !errors.Is(err, ring.ErrNativeBadInput) {
		t.Fatalf("prebound API create: %v", err)
	}
	file := raw(t, dir)
	_, err := file.ExecContext(ctx, `INSERT INTO native_sessions(
		session_id, machine_id, install_id, harness, interface, account_ref,
		profile_ref, workdir_ref, model, effort, qualification_ref, controller_id,
		generation, control_state, provider_session_id, start_effect_id)
		SELECT 'session-b', machine_id, install_id, harness, interface, account_ref,
		profile_ref, workdir_ref, model, effort, qualification_ref, controller_id,
		generation, control_state, 'provider-a', 'effect-a'
		FROM native_sessions WHERE session_id = 'session-a'`)
	if err == nil {
		t.Fatal("direct SQL created a prebound provider session")
	}
}

func TestNativeProviderBindingRejectsWrongStart(t *testing.T) {
	for _, name := range []string{"wrong session", "wrong kind", "no-effect start", "wrong generation"} {
		t.Run(name, func(t *testing.T) {
			db, _, session := nativeFixture(t)
			ctx := context.Background()
			startSession := session
			kind := ring.NativeEffectStart
			if name == "wrong session" {
				startSession.SessionID = "session-b"
				if _, err := db.CreateNativeSession(ctx, startSession); err != nil {
					t.Fatal(err)
				}
			}
			if name == "wrong kind" {
				kind = ring.NativeEffectSendTurn
			}
			effect := admitNative(t, db, startSession, "effect-a", "command-a", kind)
			if name == "no-effect start" {
				if _, err := db.ResolveNativeEffect(ctx, effect.EffectID, session.ControllerID, 1,
					ring.NativeEffectNoEffect, noEffectEvidence()); err != nil {
					t.Fatal(err)
				}
			} else if _, err := db.ClaimNativeEffect(ctx, effect.EffectID, session.ControllerID, 1); err != nil {
				t.Fatal(err)
			}
			generation := uint64(1)
			if name == "wrong generation" {
				generation = 2
			}
			if _, err := db.BindNativeProviderSession(ctx, session.SessionID, session.ControllerID,
				generation, effect.EffectID, "provider-a"); err == nil {
				t.Fatal("wrong start was bound")
			}
		})
	}
}
