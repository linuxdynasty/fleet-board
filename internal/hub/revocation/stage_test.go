package revocation_test

import (
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/revocation"
)

// Stage (row 13a slice 4) writes the file and the set without state.db:
// a retry stages nothing twice, and the next start reconciles the store.
func TestStageWritesTheFileAndTheSet(t *testing.T) {
	t.Parallel()
	live := now.Add(48 * time.Hour)
	f := newFixture(t, []serialRow{{serial: "a1", notAfter: live}}, nil)
	l, err := f.load(t, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Stage([]revocation.Entry{{Serial: "a1", NotAfter: live}}); err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if !l.Revoked("a1") {
		t.Error("the set misses the staged serial")
	}
	if got, want := f.file(t), lines(header, line("a1", live)); got != want {
		t.Errorf("file %q, want %q", got, want)
	}
	if got := f.revokedInStore(t); len(got) != 0 {
		t.Errorf("state.db revoked %v, want none (the store write follows)", got)
	}
	if err := l.Stage([]revocation.Entry{{Serial: "a1", NotAfter: live}}); err != nil {
		t.Fatalf("second Stage: %v", err)
	}
	if got, want := f.file(t), lines(header, line("a1", live)); got != want {
		t.Errorf("file after retry %q, want %q (no duplicate lines)", got, want)
	}
	again, err := f.load(t, now)
	if err != nil {
		t.Fatal(err)
	}
	if got := again.Revoked("a1"); !got {
		t.Error("a reload lost the staged serial")
	}
	if got := f.revokedInStore(t); len(got) != 1 || got[0] != "a1" {
		t.Errorf("state.db revoked %v, want [a1] (reconciled from the file)", got)
	}
}

func TestStageRefusesASerialThatCannotMatch(t *testing.T) {
	t.Parallel()
	live := now.Add(48 * time.Hour)
	f := newFixture(t, []serialRow{{serial: "a1", notAfter: live}}, nil)
	l, err := f.load(t, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Stage([]revocation.Entry{{Serial: "A1", NotAfter: live}}); err == nil {
		t.Error("Stage accepted an uppercase serial")
	}
	if l.Revoked("A1") {
		t.Error("the set holds the refused serial")
	}
}
