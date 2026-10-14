package codex

import (
	"strings"
	"testing"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// TestParseReadyTrust reads the trust screen (01 C3): the folder-access box
// is the FOLDER_UNTRUSTED signal, so it must read as trust on both versions.
// Any other screen is not a trust screen and fails cleanly: prompt detection
// is a later slice, and a guess would answer the wrong question. The update
// and daemon-settings startup screens are the near misses that must not
// read as trust.
func TestParseReadyTrust(t *testing.T) {
	trust160 := loadScreen(t, "0.160.0", "trust-folder-git.txt")
	trust162 := loadScreen(t, "0.162.1", "trust-folder-git.txt")
	ready := loadScreen(t, "0.162.1", "startup-ready.txt")
	status := loadScreen(t, "0.162.1", "status.txt")
	update := loadScreen(t, "0.160.0", "startup-update-prompt.txt")
	daemon := loadScreen(t, "0.160.0", "startup-daemon-settings-incompatible.txt")
	cases := []struct {
		name      string
		screen    string
		wantTrust bool
		wantOK    bool
	}{
		{"trust folder 0.160.0", trust160, true, true},
		{"trust folder 0.162.1", trust162, true, true},
		{"ready prompt is not trust", ready, false, false},
		{"status card is not trust", status, false, false},
		{"update prompt is not trust", update, false, false},
		{"daemon settings screen is not trust", daemon, false, false},
		{"empty screen is not trust", "", false, false},
		{"title without the answer row is not trust", "Folder access\n  ~/scratch\n", false, false},
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
