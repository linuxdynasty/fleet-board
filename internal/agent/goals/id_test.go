package goals_test

import (
	"strings"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/goals"
)

func TestMintID(t *testing.T) {
	t.Parallel()
	// Vector worked out with sha256sum and python base64.b32encode, not
	// with the implementation.
	got := goals.MintID("m1", "/repos/a/.claude/active-goal.s1", time.Unix(0, 1000).UTC())
	if got != "obs-mmr5xpm7vcsd3piwju76qpe6ki" {
		t.Fatalf("id = %q, want the spec vector", got)
	}
	for _, c := range got[4:] {
		if !strings.ContainsRune("abcdefghijklmnopqrstuvwxyz234567", c) {
			t.Fatalf("id = %q, want lowercase base32 after obs-", got)
		}
	}
	other := goals.MintID("m1", "/repos/a/.claude/active-goal.s1", time.Unix(0, 2000).UTC())
	if other == got {
		t.Fatal("a new first-seen time mints the same id")
	}
}
