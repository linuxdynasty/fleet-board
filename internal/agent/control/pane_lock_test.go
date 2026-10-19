package control

import (
	"testing"
	"time"
)

// waitPaneRefs is a state barrier: it proves a waiter has registered before
// the holder releases the pane. A sleep alone would not prove that ordering.
func waitPaneRefs(t *testing.T, e *Ensurer, id string, want int) *paneLock {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		e.paneMu.Lock()
		entry := e.panes[id]
		refs := 0
		if entry != nil {
			refs = entry.refs
		}
		e.paneMu.Unlock()
		if refs == want {
			return entry
		}
		select {
		case <-deadline:
			t.Fatalf("pane %s has %d references, want %d", id, refs, want)
		case <-time.After(time.Millisecond):
		}
	}
}

func waitLockStep(t *testing.T, ch <-chan struct{}, step string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", step)
	}
}

func TestPaneLockWaitersShareOneEntryThroughRelease(t *testing.T) {
	e, err := New(testOptions())
	if err != nil {
		t.Fatal(err)
	}
	unlockFirst := e.lockPane("%1")
	firstEntry := waitPaneRefs(t, e, "%1", 1)
	heldSecond := make(chan struct{})
	releaseSecond := make(chan struct{})
	doneSecond := make(chan struct{})
	go func() {
		unlock := e.lockPane("%1")
		close(heldSecond)
		<-releaseSecond
		unlock()
		close(doneSecond)
	}()
	if got := waitPaneRefs(t, e, "%1", 2); got != firstEntry {
		t.Fatal("same-pane waiter registered on a different entry")
	}
	select {
	case <-heldSecond:
		t.Fatal("same-pane waiter entered while the first holder still owned the pane")
	default:
	}

	// A separate pane can proceed while %1 is held and has a waiter.
	otherDone := make(chan struct{})
	go func() {
		e.lockPane("%2")()
		close(otherDone)
	}()
	waitLockStep(t, otherDone, "independent pane")
	unlockFirst()
	waitLockStep(t, heldSecond, "second same-pane holder")
	if got := waitPaneRefs(t, e, "%1", 1); got != firstEntry {
		t.Fatal("entry changed while the second holder owned the pane")
	}

	heldThird := make(chan struct{})
	releaseThird := make(chan struct{})
	doneThird := make(chan struct{})
	go func() {
		unlock := e.lockPane("%1")
		close(heldThird)
		<-releaseThird
		unlock()
		close(doneThird)
	}()
	if got := waitPaneRefs(t, e, "%1", 2); got != firstEntry {
		t.Fatal("third command registered on a different entry before the second released it")
	}
	select {
	case <-heldThird:
		t.Fatal("third command entered while the second holder still owned the pane")
	default:
	}
	close(releaseSecond)
	waitLockStep(t, doneSecond, "second holder release")
	waitLockStep(t, heldThird, "third same-pane holder")
	if got := waitPaneRefs(t, e, "%1", 1); got != firstEntry {
		t.Fatal("entry changed while the third holder owned the pane")
	}
	close(releaseThird)
	waitLockStep(t, doneThird, "third holder release")
	e.paneMu.Lock()
	retained := len(e.panes)
	e.paneMu.Unlock()
	if retained != 0 {
		t.Fatalf("%d pane locks remain after all holders and waiters left", retained)
	}
}
