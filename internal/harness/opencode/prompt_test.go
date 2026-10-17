package opencode

import (
	"strings"
	"testing"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// TestParsePromptUnknown reads real OpenCode screens for E6 prompt
// detection (01 E6): no prompt shape is verified yet, so every screen
// without a verified shape fails cleanly instead of guessing. Prompt
// fixtures need a live probe with model calls (quota), so until they
// land the parser holds the seam and answers stay notice-only
// (answerable=false, row 19).
func TestParsePromptUnknown(t *testing.T) {
	screens := map[string]string{
		"spend table":    readFixture(t, "1.19.0", "stats.txt"),
		"status dialog":  readFixture(t, "1.19.0", "tui-status-dialog.txt"),
		"ready tui":      readFixture(t, "1.19.0", "tui-ready.txt"),
		"ready older":    readFixture(t, "1.18.34", "tui-ready.txt"),
		"empty screen":   "",
		"question words": "Share this file?\n",
		"oversize":       strings.Repeat("x", 65*1024),
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
