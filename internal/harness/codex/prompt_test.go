package codex

import (
	"strings"
	"testing"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// TestParsePromptUnknown reads real Codex screens for E6 prompt detection
// (01 E6): no prompt shape is verified yet, so every screen without a
// verified shape fails cleanly instead of guessing. Permission prompt
// fixtures need a live probe with model calls (quota), so until they
// land the parser holds the seam and answers stay notice-only
// (answerable=false, row 19).
func TestParsePromptUnknown(t *testing.T) {
	screens := map[string]string{
		"status card":        loadScreen(t, "0.160.0", "status.txt"),
		"trust folder":       loadScreen(t, "0.160.0", "trust-folder-git.txt"),
		"update prompt":      loadScreen(t, "0.160.0", "startup-update-prompt.txt"),
		"daemon settings":    loadScreen(t, "0.160.0", "startup-daemon-settings-incompatible.txt"),
		"ready 0.162.1":      loadScreen(t, "0.162.1", "startup-ready.txt"),
		"trust folder 0.162": loadScreen(t, "0.162.1", "trust-folder-git.txt"),
		"empty screen":       "",
		"question words":     "Should this command run?\n",
		"oversize":           strings.Repeat("x", 65*1024),
	}
	for name, screen := range screens {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := ParsePrompt(screen)
			if got.ParseOutcome != typev1.ParseOutcome_PARSE_OUTCOME_FAILED {
				t.Errorf("ParseOutcome = %v, want FAILED", got.ParseOutcome)
			}
			if got.Text != "" || len(got.Options) != 0 || got.Destructive {
				t.Errorf("failed parse carries prompt content: %+v", got)
			}
			if got.Confidence != 0 {
				t.Errorf("Confidence = %d, want 0", got.Confidence)
			}
		})
	}
}
