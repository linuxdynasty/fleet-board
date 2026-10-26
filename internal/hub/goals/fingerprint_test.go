package goals

import (
	"testing"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// TestFingerprintFailsFirst proves the transition fingerprint before it
// is used: equal states hash equal, any input moves the hash, and the
// shape is a hex digest (03l "Fingerprint").
func TestFingerprintFailsFirst(t *testing.T) {
	t.Parallel()
	a := Fingerprint(typev1.GoalLineState_GOAL_LINE_STATE_WORKING, "fix the leak", true, "abc123", "open", "def456")
	b := Fingerprint(typev1.GoalLineState_GOAL_LINE_STATE_WORKING, "fix the leak", true, "abc123", "open", "def456")
	if a == "" || a != b {
		t.Fatalf("fingerprint not stable: %q vs %q", a, b)
	}
	if len(a) != 64 {
		t.Fatalf("fingerprint %q is not a hex sha256", a)
	}
	for _, other := range []string{
		Fingerprint(typev1.GoalLineState_GOAL_LINE_STATE_DONE, "fix the leak", true, "abc123", "open", "def456"),
		Fingerprint(typev1.GoalLineState_GOAL_LINE_STATE_WORKING, "fix the leak!", true, "abc123", "open", "def456"),
		Fingerprint(typev1.GoalLineState_GOAL_LINE_STATE_WORKING, "fix the leak", false, "abc123", "open", "def456"),
		Fingerprint(typev1.GoalLineState_GOAL_LINE_STATE_WORKING, "fix the leak", true, "abc124", "open", "def456"),
		Fingerprint(typev1.GoalLineState_GOAL_LINE_STATE_WORKING, "fix the leak", true, "abc123", "merged", "def456"),
		Fingerprint(typev1.GoalLineState_GOAL_LINE_STATE_WORKING, "fix the leak", true, "abc123", "open", "def457"),
	} {
		if other == a {
			t.Fatalf("fingerprint did not move for a changed input")
		}
	}
}
