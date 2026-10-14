package muse

import (
	"strings"
	"testing"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// TestParseReadyTrust reads the trust screen (01 C3): the workspace-trust
// box is the FOLDER_UNTRUSTED signal, so it must read as trust on every
// version, including the update-banner and git-root shapes. Any other screen
// is not a trust screen and fails cleanly: prompt detection is a later
// slice, and a guess would answer the wrong question.
func TestParseReadyTrust(t *testing.T) {
	trust144 := loadScreen(t, "1.4.4", "trust-workspace.txt")
	trust144plain := loadScreen(t, "1.4.4", "trust-workspace-plain-folder.txt")
	trust144git := loadScreen(t, "1.4.4", "trust-workspace-git-root.txt")
	trust146 := loadScreen(t, "1.4.6", "trust-workspace.txt")
	trust147 := loadScreen(t, "1.4.7", "trust-workspace.txt")
	ready := loadScreen(t, "1.4.7", "startup-ready.txt")
	usage := loadScreen(t, "1.4.7", "usage.txt")
	cases := []struct {
		name      string
		screen    string
		wantTrust bool
		wantOK    bool
	}{
		{"trust workspace 1.4.4", trust144, true, true},
		{"trust plain folder 1.4.4", trust144plain, true, true},
		{"trust git root 1.4.4", trust144git, true, true},
		{"trust workspace 1.4.6", trust146, true, true},
		{"trust workspace 1.4.7", trust147, true, true},
		{"ready prompt is not trust", ready, false, false},
		{"usage box is not trust", usage, false, false},
		{"empty screen is not trust", "", false, false},
		{"title without the answer row is not trust", "Do you trust this workspace?\nWorkspace: ~/scratch\n", false, false},
		{"oversize screen fails", strings.Repeat("x", 65*1024), false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ParseReady(tc.screen)
			if got.TrustBox != tc.wantTrust {
				t.Errorf("TrustBox = %v, want %v", got.TrustBox, tc.wantTrust)
			}
			wantOutcome := typev1.ParseOutcome_PARSE_OUTCOME_FAILED
			if tc.wantOK {
				wantOutcome = typev1.ParseOutcome_PARSE_OUTCOME_OK
			}
			if got.ParseOutcome != wantOutcome {
				t.Errorf("ParseOutcome = %v, want %v", got.ParseOutcome, wantOutcome)
			}
		})
	}
}
