package codex

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

func loadScreen(t *testing.T, version, name string) string {
	t.Helper()
	//nolint:gosec // G304: a fixture under the package's own testdata, named by the test, test code only
	raw, err := os.ReadFile(filepath.Join("testdata", version, name))
	if err != nil {
		t.Fatalf("read fixture %s/%s: %v", version, name, err)
	}
	return string(raw)
}

// Codex shows "% left"; the standardized bucket carries "% used" and
// keeps that the screen showed the remainder (01 D2). Both fixture
// captures read 0% left: the account was out of weekly quota.
func TestParseUsageFixture(t *testing.T) {
	cases := []struct {
		version  string
		readTime time.Time
		plan     string
		resetDay int
		resetH   int
		resetMin int
		shown    string
	}{
		{
			"0.160.0", time.Date(2026, time.October, 10, 12, 0, 0, 0, time.Local),
			"Pro 200", 14, 5, 17, "resets 5:17 AM on 14 Oct",
		},
		{
			"0.162.1", time.Date(2026, time.October, 12, 12, 0, 0, 0, time.Local),
			"Pro 200", 17, 23, 57, "resets 11:57 PM on 17 Oct",
		},
	}
	for _, tc := range cases {
		t.Run(tc.version, func(t *testing.T) {
			got := ParseUsage(loadScreen(t, tc.version, "status.txt"), tc.readTime)

			if got.ParseOutcome != typev1.ParseOutcome_PARSE_OUTCOME_OK {
				t.Fatalf("outcome = %v, want OK", got.ParseOutcome)
			}
			if got.Plan != tc.plan {
				t.Errorf("plan = %q, want %q", got.Plan, tc.plan)
			}
			if len(got.Buckets) != 1 {
				t.Fatalf("buckets = %d, want 1", len(got.Buckets))
			}
			b := got.Buckets[0]
			if b.GetKey() != "weekly" {
				t.Errorf("key = %q, want weekly", b.GetKey())
			}
			if b.GetLabel() != "Weekly limit" {
				t.Errorf("label = %q, want Weekly limit", b.GetLabel())
			}
			if b.GetKind() != typev1.WindowKind_WINDOW_KIND_WEEKLY {
				t.Errorf("kind = %v, want WEEKLY", b.GetKind())
			}
			if !b.GetShownAsRemaining() {
				t.Errorf("Codex shows percent left, so shown_as_remaining must be set")
			}
			p, ok := b.GetPercentUsed().GetKind().(*typev1.Percent_Value)
			if !ok {
				t.Fatalf("percent is not a value: %v", b.GetPercentUsed())
			}
			if p.Value != 100 {
				t.Errorf("percent used = %d, want 100 (0%% left)", p.Value)
			}
			if b.GetResetsTime() == nil {
				t.Fatal("bucket has no reset")
			}
			rt := b.GetResetsTime().GetResolvedTime().AsTime().In(time.Local)
			y, m, d := rt.Date()
			h, min, _ := rt.Clock()
			if y != 2026 || m != time.October || d != tc.resetDay || h != tc.resetH || min != tc.resetMin {
				t.Errorf("reset = %v local, want 2026-10-%d %02d:%02d local", rt, tc.resetDay, tc.resetH, tc.resetMin)
			}
			if b.GetResetsTime().GetOriginalText() != tc.shown {
				t.Errorf("reset text = %q, want %q", b.GetResetsTime().GetOriginalText(), tc.shown)
			}
			if b.GetResetsTime().GetResolvedWithTimezone() == "" {
				t.Errorf("reset names no timezone")
			}
			if b.GetWindow() != nil {
				t.Errorf("window is set; the screen shows no window length")
			}
		})
	}
}

// The 0.162.1 account line carries the sign-in email next to the plan.
// The plan comes from the words in brackets; an email is never a plan.
func TestParseUsagePlan(t *testing.T) {
	base := loadScreen(t, "0.162.1", "status.txt")
	readTime := time.Date(2026, time.October, 12, 12, 0, 0, 0, time.Local)

	emailOnly := strings.ReplaceAll(base, "user@example.com (Pro 200)", "user@example.com")
	if got := ParseUsage(emailOnly, readTime); got.Plan != "" {
		t.Errorf("plan from an email-only account line = %q, want empty", got.Plan)
	}
	noAccount := dropLines(base, "Account:")
	got := ParseUsage(noAccount, readTime)
	if got.Plan != "" {
		t.Errorf("plan with no account line = %q, want empty", got.Plan)
	}
	if got.ParseOutcome != typev1.ParseOutcome_PARSE_OUTCOME_OK {
		t.Errorf("outcome with no account line = %v, want OK (the limit row still read)", got.ParseOutcome)
	}
}

func dropLines(screen string, want string) string {
	var kept []string
	for line := range strings.Lines(strings.TrimSuffix(screen, "\n")) {
		if !strings.Contains(line, want) {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n") + "\n"
}

// A weekly row that no longer reads is partial with an unknown percent,
// never a nearby number from elsewhere on the card.
func TestParseUsageUnreadableWeekly(t *testing.T) {
	screen := strings.ReplaceAll(loadScreen(t, "0.160.0", "status.txt"),
		"0% left (resets 5:17 AM on 14 Oct)", "see the dashboard")
	got := ParseUsage(screen, time.Date(2026, time.October, 10, 12, 0, 0, 0, time.Local))

	if got.ParseOutcome != typev1.ParseOutcome_PARSE_OUTCOME_PARTIAL {
		t.Fatalf("outcome = %v, want PARTIAL", got.ParseOutcome)
	}
	if len(got.Buckets) != 1 {
		t.Fatalf("buckets = %d, want 1", len(got.Buckets))
	}
	if r := got.Buckets[0].GetPercentUsed().GetUnknown().GetReason(); r != typev1.UnknownReason_UNKNOWN_REASON_PARSE_FAILED {
		t.Errorf("percent reason = %v, want PARSE_FAILED", r)
	}
}

// A screen with no status card fails cleanly: no buckets, no numbers.
func TestParseUsageNoCard(t *testing.T) {
	for name, screen := range map[string]string{
		"garbage":       "hello\n",
		"ready screen":  loadScreen(t, "0.162.1", "startup-ready.txt"),
		"command typed": loadScreen(t, "0.160.0", "status-command-typed.txt"),
		"oversize":      strings.Repeat("x", 65*1024),
	} {
		got := ParseUsage(screen, time.Date(2026, time.October, 12, 12, 0, 0, 0, time.Local))
		if got.ParseOutcome != typev1.ParseOutcome_PARSE_OUTCOME_FAILED {
			t.Errorf("%s: outcome = %v, want FAILED", name, got.ParseOutcome)
		}
		if len(got.Buckets) != 0 {
			t.Errorf("%s: buckets = %d, want 0", name, len(got.Buckets))
		}
	}
}
