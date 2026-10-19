package ring_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/agent/ring"
)

func noEffectEvidence() ring.NativeEvidence {
	return ring.NativeEvidence{
		Kind:   ring.NativeEvidencePrelaunch,
		Digest: nativeDigest([]byte("verified prelaunch refusal")),
	}
}

func TestNativeRetryNeedsOneProvenPredecessor(t *testing.T) {
	db, _, session := nativeFixture(t)
	ctx := context.Background()
	first := admitNative(t, db, session, "effect-a", "command-a", ring.NativeEffectStart)
	if _, err := db.ResolveNativeEffect(ctx, first.EffectID, session.ControllerID, 1,
		ring.NativeEffectNoEffect, noEffectEvidence()); err != nil {
		t.Fatal(err)
	}
	body := []byte("different command envelope for the retry")
	nativeCommandKind(t, db, "command-b", "native.start", body)
	retry := ring.NativeEffectRecord{
		EffectID: "effect-b", CommandID: "command-b",
		SessionID: session.SessionID, Generation: 1, Kind: ring.NativeEffectStart,
		CommandBodyDigest: nativeDigest(body), RetryOfEffectID: first.EffectID,
	}
	got, dup, err := db.AdmitNativeEffect(ctx, retry)
	if err != nil || dup || got.RetryOfEffectID != first.EffectID || got.CommandBodyDigest == first.CommandBodyDigest {
		t.Fatalf("linked retry: %+v dup=%v err=%v", got, dup, err)
	}
	nativeCommandKind(t, db, "command-c", "native.start", []byte("sibling retry"))
	sibling := retry
	sibling.EffectID, sibling.CommandID = "effect-c", "command-c"
	sibling.CommandBodyDigest = nativeDigest([]byte("sibling retry"))
	if _, _, err := db.AdmitNativeEffect(ctx, sibling); !errors.Is(err, ring.ErrNativeConflict) {
		t.Fatalf("second successor: %v", err)
	}
	if _, _, err := db.AdmitNativeEffect(ctx, ring.NativeEffectRecord{
		EffectID: "effect-self", CommandID: "command-c", SessionID: session.SessionID,
		Generation: 1, Kind: ring.NativeEffectStart, CommandBodyDigest: sibling.CommandBodyDigest,
		RetryOfEffectID: "effect-self",
	}); !errors.Is(err, ring.ErrNativeConflict) {
		t.Fatalf("self-link: %v", err)
	}
}

func TestNativeConcurrentSiblingRetriesHaveOneWinner(t *testing.T) {
	db, _, session := nativeFixture(t)
	ctx := context.Background()
	first := admitNative(t, db, session, "effect-a", "command-a", ring.NativeEffectStart)
	if _, err := db.ResolveNativeEffect(ctx, first.EffectID, session.ControllerID, 1,
		ring.NativeEffectNoEffect, noEffectEvidence()); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"b", "c"} {
		nativeCommandKind(t, db, "command-"+id, "native.start", []byte(id))
	}
	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, id := range []string{"b", "c"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, _, err := db.AdmitNativeEffect(ctx, ring.NativeEffectRecord{
				EffectID: "effect-" + id, CommandID: "command-" + id,
				SessionID: session.SessionID, Generation: 1, Kind: ring.NativeEffectStart,
				CommandBodyDigest: nativeDigest([]byte(id)), RetryOfEffectID: first.EffectID,
			})
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	var admitted, refused int
	for err := range results {
		switch {
		case err == nil:
			admitted++
		case errors.Is(err, ring.ErrNativeConflict):
			refused++
		default:
			t.Fatalf("retry result: %v", err)
		}
	}
	if admitted != 1 || refused != 1 {
		t.Fatalf("admitted=%d refused=%d, want one each", admitted, refused)
	}
}

func TestNativeRetryRejectsWrongLineage(t *testing.T) {
	for _, name := range []string{"wrong generation", "wrong session", "wrong kind", "predecessor still prepared"} {
		t.Run(name, func(t *testing.T) {
			db, _, session := nativeFixture(t)
			ctx := context.Background()
			first := admitNative(t, db, session, "effect-a", "command-a", ring.NativeEffectStart)
			if name != "predecessor still prepared" {
				if _, err := db.ResolveNativeEffect(ctx, first.EffectID, session.ControllerID, 1,
					ring.NativeEffectNoEffect, noEffectEvidence()); err != nil {
					t.Fatal(err)
				}
			}
			attempt := ring.NativeEffectRecord{
				EffectID: "effect-b", CommandID: "command-b",
				SessionID: session.SessionID, Generation: 1, Kind: ring.NativeEffectStart,
				RetryOfEffectID: first.EffectID,
			}
			switch name {
			case "wrong generation":
				attempt.Generation = 2
			case "wrong session":
				other := session
				other.SessionID = "session-b"
				if _, err := db.CreateNativeSession(ctx, other); err != nil {
					t.Fatal(err)
				}
				attempt.SessionID = other.SessionID
			case "wrong kind":
				attempt.Kind = ring.NativeEffectResume
			}
			body := []byte("a new command")
			attempt.CommandBodyDigest = nativeDigest(body)
			nativeCommandKind(t, db, attempt.CommandID, "native."+string(attempt.Kind), body)
			if _, _, err := db.AdmitNativeEffect(ctx, attempt); err == nil {
				t.Fatal("invalid retry lineage was admitted")
			}
		})
	}
}
