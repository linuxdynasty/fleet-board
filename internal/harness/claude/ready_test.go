package claude

import (
	"strings"
	"testing"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// TestParseReadyTrust reads the trust screen (01 C3): the folder-trust box
// is the FOLDER_UNTRUSTED signal, so it must read as trust. Any other
// screen is not a trust screen and fails cleanly: prompt detection is a
// later slice, and a guess would answer the wrong question.
func TestParseReadyTrust(t *testing.T) {
	trust := loadScreen(t, "2.1.296", "trust-folder.txt")
	ready := loadScreen(t, "2.1.296", "startup-ready.txt")
	usage := loadScreen(t, "2.1.296", "usage.txt")
	cases := []struct {
		name      string
		screen    string
		wantTrust bool
		wantOK    bool
	}{
		{"trust folder", trust, true, true},
		{"ready prompt is not trust", ready, false, false},
		{"usage dialog is not trust", usage, false, false},
		{"empty screen is not trust", "", false, false},
		{"title without the answer row is not trust", "Accessing workspace:\n\n ~/scratch\n", false, false},
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
