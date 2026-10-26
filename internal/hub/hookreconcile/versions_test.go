package hookreconcile

import (
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/agent/hooks"
)

// TestExpectedVersionsMatchAgent pins the hub's expected hook versions to
// the agent's installed version: the "behind" gate compares equal strings,
// so a skew here would reinstall forever or never.
func TestExpectedVersionsMatchAgent(t *testing.T) {
	versions := ExpectedVersions()
	for _, h := range []string{"claude", "codex", "muse", "cursor"} {
		if versions[h] != hooks.HookVersion {
			t.Fatalf("ExpectedVersions[%s] = %q, want agent HookVersion %q", h, versions[h], hooks.HookVersion)
		}
	}
	if len(versions) != 4 {
		t.Fatalf("ExpectedVersions covers %d harnesses, want exactly the 4 hook shapes", len(versions))
	}
}
