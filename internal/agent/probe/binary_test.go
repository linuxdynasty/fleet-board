package probe

import "testing"

func TestHarnessBinary(t *testing.T) {
	t.Parallel()
	for _, h := range []string{"claude", "codex", "muse", "cursor"} {
		if _, ok := HarnessBinary(h); !ok {
			t.Fatalf("HarnessBinary(%q) missing", h)
		}
	}
	if _, ok := HarnessBinary("opencode"); ok {
		t.Fatal("OpenCode takes no harness session: no binary")
	}
	if _, ok := HarnessBinary("gemini"); ok {
		t.Fatal("unknown harness has no binary")
	}
}
