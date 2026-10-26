package main

import (
	"context"
	"errors"
	"testing"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/startgoal"
)

type fakePanes struct {
	top   string
	err   error
	push  bool
	known bool
}

func (f fakePanes) Examine(context.Context, *typev1.PaneKey) (startgoal.PaneState, error) {
	return startgoal.PaneState{}, nil
}

func (f fakePanes) LookupRepo(*typev1.RepoRef) (string, error) {
	return f.top, f.err
}

func (f fakePanes) PushAccess(context.Context, *typev1.RepoRef) (bool, bool) {
	return f.push, f.known
}

func preflightSpec() *agentv1.GoalSpec {
	return &agentv1.GoalSpec{
		Harness: &typev1.Harness{Name: "Muse"},
		Repo:    &typev1.RepoRef{Host: "github.com", Path: "acme/api"},
	}
}

// TestPreflightReaderMapsProductionSeams: the checkout table, forge
// access, and disk sample land in the Facts the checker judges; sign-in
// stays unknown (skipped, never failed) until snapshot accounts exist.
func TestPreflightReaderMapsProductionSeams(t *testing.T) {
	r := preflightReader{
		panes:    fakePanes{top: "/wt/one", push: true, known: true},
		diskFree: func() (int64, bool) { return 20 * 1024 * 1024 * 1024, true },
	}
	facts, err := r.Facts(context.Background(), preflightSpec())
	if err != nil {
		t.Fatalf("Facts = %v", err)
	}
	if facts.SignedInKnown {
		t.Fatal("production sign-in is unknown: nothing reads a harness screen pre-pane")
	}
	if !facts.CheckoutFound || !facts.PushKnown || !facts.PushAccess {
		t.Fatalf("Facts = %+v, want checkout found with push access", facts)
	}
	if !facts.DiskKnown || facts.DiskFreeBytes != 20*1024*1024*1024 {
		t.Fatalf("Facts = %+v, want the disk sample carried through", facts)
	}
	if facts.FolderUntrusted {
		t.Fatal("nothing is open to prompt pre-pane: FolderUntrusted must be false")
	}
}

// TestPreflightReaderMissingCheckout: no checkout is a fact (the checker
// refuses CHECKOUT_MISSING), not a reader error.
func TestPreflightReaderMissingCheckout(t *testing.T) {
	r := preflightReader{
		panes:    fakePanes{err: errors.New("no local checkout")},
		diskFree: func() (int64, bool) { return 0, false },
	}
	facts, err := r.Facts(context.Background(), preflightSpec())
	if err != nil {
		t.Fatalf("Facts = %v, want a fact, not an error", err)
	}
	if facts.CheckoutFound || facts.PushKnown || facts.DiskKnown {
		t.Fatalf("Facts = %+v, want everything missing/unknown", facts)
	}
}
