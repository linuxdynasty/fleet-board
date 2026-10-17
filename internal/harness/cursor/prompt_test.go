package cursor

import (
	"strings"
	"testing"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// TestParsePromptUnknown reads real cursor-agent screens for E6 prompt
// detection (01 E6): no prompt shape is verified yet, so every screen
// without a verified shape fails cleanly instead of guessing. Permission
// prompts under each approval mode are still unprobed (01b), and probing
// them needs model calls (quota), so until fixtures land the parser holds
// the seam and answers stay notice-only (answerable=false, row 19).
func TestParsePromptUnknown(t *testing.T) {
	screens := map[string]string{
		"usage dialog":    loadScreen(t, "usage.txt"),
		"trust workspace": loadScreen(t, "trust-workspace.txt"),
		"ready followup":  loadScreen(t, "ready-followup-placeholder.txt"),
		"status":          loadScreen(t, "status.txt"),
		"empty screen":    "",
		"question words":  "Approve this command?\n",
		"oversize":        strings.Repeat("x", 65*1024),
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
