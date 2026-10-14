package probe

import (
	"strings"
	"testing"
	"time"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// TestBuildReading assembles readings from the parsers' own usage
// screens: buckets ride through, the source is PROBE, ids echo, and the
// plan line is scrubbed (Codex 0.162.1 prints the sign-in email there).
func TestBuildReading(t *testing.T) {
	t.Parallel()
	pane := &typev1.PaneKey{Server: "fleet-probe", ServerStartId: "s", PaneId: "%0"}
	probeID := &typev1.ProbeId{Value: "probe-1"}
	accountKey := &typev1.AccountKey{Value: "key-1"}
	now := time.Date(2026, time.October, 14, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name     string
		harness  string
		file     string
		scrubbed bool // the screen shows an email address
	}{
		{name: "claude", harness: "claude", file: "../../harness/claude/testdata/2.1.296/usage.txt"},
		{name: "codex", harness: "codex", file: "../../harness/codex/testdata/0.160.0/status.txt"},
		{name: "codex email plan", harness: "codex", file: "../../harness/codex/testdata/0.162.1/status.txt", scrubbed: true},
		{name: "muse", harness: "muse", file: "../../harness/muse/testdata/1.4.6/usage.txt"},
		{name: "cursor", harness: "cursor", file: "../../harness/cursor/testdata/2026.10.01/usage.txt"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			screen := loadScreen(t, tc.file)
			r, ok := BuildReading(tc.harness, screen, now, pane, probeID, accountKey, &typev1.Harness{Name: tc.harness})
			if !ok {
				t.Fatalf("BuildReading(%s) = false", tc.harness)
			}
			if r.GetParse() != typev1.ParseOutcome_PARSE_OUTCOME_OK && r.GetParse() != typev1.ParseOutcome_PARSE_OUTCOME_PARTIAL {
				t.Errorf("parse = %v, want OK or PARTIAL", r.GetParse())
			}
			if len(r.GetBuckets()) == 0 {
				t.Errorf("no buckets from a usage screen")
			}
			if r.GetSource() != typev1.UsageSource_USAGE_SOURCE_PROBE {
				t.Errorf("source = %v, want PROBE", r.GetSource())
			}
			if r.GetPane() != pane || r.GetProbeId() != probeID || r.GetAccountKey() != accountKey {
				t.Errorf("ids do not echo the pass")
			}
			if r.GetParserVersion() == "" {
				t.Errorf("no parser version")
			}
			if tc.scrubbed && strings.Contains(r.GetShown().GetRawScreen(), "@") {
				t.Errorf("raw screen keeps an email address")
			}
			for _, f := range r.GetShown().GetFields() {
				if strings.Contains(f.GetValue(), "@") {
					t.Errorf("shown field %q keeps an email address: %q", f.GetLabel(), f.GetValue())
				}
			}
		})
	}
}

// TestBuildReadingNoParser refuses harnesses with no usage parser instead
// of guessing a reading.
func TestBuildReadingNoParser(t *testing.T) {
	t.Parallel()
	if _, ok := BuildReading("opencode", "anything", time.Now(), nil, nil, nil, nil); ok {
		t.Errorf("BuildReading(opencode) = true")
	}
}

// TestBuildReadingFailedStillReports turns an unreadable screen into a
// PARSE_FAILED reading with its redacted raw screen, never a guess.
func TestBuildReadingFailedStillReports(t *testing.T) {
	t.Parallel()
	r, ok := BuildReading("codex", "garbage with no weekly row", time.Now(), nil, nil, nil, nil)
	if !ok {
		t.Fatalf("BuildReading = false")
	}
	if r.GetParse() != typev1.ParseOutcome_PARSE_OUTCOME_FAILED {
		t.Errorf("parse = %v, want FAILED", r.GetParse())
	}
	if len(r.GetBuckets()) != 0 {
		t.Errorf("buckets = %d, want none on FAILED", len(r.GetBuckets()))
	}
	if r.GetShown().GetRawScreen() == "" {
		t.Errorf("FAILED reading carries no raw screen")
	}
}
