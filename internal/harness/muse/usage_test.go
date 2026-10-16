package muse

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

// Every fixture version reads the same two buckets off its own screen:
// "Current" (the short window) and "Weekly", each with its reset. The
// read times are the captures' own "as of" times and dates.
func TestParseUsageFixture(t *testing.T) {
	cases := []struct {
		version       string
		readTime      time.Time
		current       uint32
		weekly        uint32
		currentResetH int
		currentResetM int
		weeklyDay     int
		currentShown  string
		weeklyShown   string
	}{
		{
			"1.4.4", time.Date(2026, time.October, 10, 16, 47, 0, 0, time.Local),
			0, 14, 18, 47, 11, "Resets at 6:47 PM", "Resets Oct 11 at 8:00 PM",
		},
		{
			"1.4.6", time.Date(2026, time.October, 12, 14, 26, 0, 0, time.Local),
			4, 11, 17, 24, 18, "Resets at 5:24 PM", "Resets Oct 18 at 8:00 PM",
		},
		{
			"1.4.7", time.Date(2026, time.October, 12, 20, 50, 0, 0, time.Local),
			17, 5, 22, 24, 18, "Resets at 10:24 PM", "Resets Oct 18 at 8:00 PM",
		},
	}
	for _, tc := range cases {
		t.Run(tc.version, func(t *testing.T) {
			got := ParseUsage(loadScreen(t, tc.version, "usage.txt"), tc.readTime)

			if got.ParseOutcome != typev1.ParseOutcome_PARSE_OUTCOME_OK {
				t.Fatalf("outcome = %v, want OK", got.ParseOutcome)
			}
			if got.Plan != "Muse Code Example Plan" {
				t.Errorf("plan = %q, want the subscription line's plan", got.Plan)
			}
			if len(got.Buckets) != 2 {
				t.Fatalf("buckets = %d, want 2", len(got.Buckets))
			}
			cur, wk := got.Buckets[0], got.Buckets[1]
			if cur.GetKey() != "current" || cur.GetLabel() != "Current" ||
				cur.GetKind() != typev1.WindowKind_WINDOW_KIND_SHORT {
				t.Errorf("first bucket = %q/%q/%v, want current/Current/SHORT",
					cur.GetKey(), cur.GetLabel(), cur.GetKind())
			}
			if wk.GetKey() != "weekly" || wk.GetLabel() != "Weekly" ||
				wk.GetKind() != typev1.WindowKind_WINDOW_KIND_WEEKLY {
				t.Errorf("second bucket = %q/%q/%v, want weekly/Weekly/WEEKLY",
					wk.GetKey(), wk.GetLabel(), wk.GetKind())
			}
			if p := percentValue(t, cur); p != tc.current {
				t.Errorf("current percent = %d, want %d", p, tc.current)
			}
			if p := percentValue(t, wk); p != tc.weekly {
				t.Errorf("weekly percent = %d, want %d", p, tc.weekly)
			}
			// The Current reset is later the same day; the Weekly reset
			// is at 8:00 PM on its shown date, both machine-local.
			cr := resetLocal(t, cur)
			if cr.Day() != tc.readTime.Day() || cr.Hour() != tc.currentResetH || cr.Minute() != tc.currentResetM {
				t.Errorf("current reset = %v local, want day %d at %02d:%02d",
					cr, tc.readTime.Day(), tc.currentResetH, tc.currentResetM)
			}
			wr := resetLocal(t, wk)
			if wr.Month() != time.October || wr.Day() != tc.weeklyDay || wr.Hour() != 20 || wr.Minute() != 0 {
				t.Errorf("weekly reset = %v local, want Oct %d at 20:00", wr, tc.weeklyDay)
			}
			if cur.GetResetsTime().GetOriginalText() != tc.currentShown {
				t.Errorf("current reset text = %q, want %q", cur.GetResetsTime().GetOriginalText(), tc.currentShown)
			}
			if wk.GetResetsTime().GetOriginalText() != tc.weeklyShown {
				t.Errorf("weekly reset text = %q, want %q", wk.GetResetsTime().GetOriginalText(), tc.weeklyShown)
			}
			for _, b := range got.Buckets {
				if b.GetWindow() != nil {
					t.Errorf("bucket %s window is set; the screen shows no window length", b.GetKey())
				}
				if b.GetShownAsRemaining() {
					t.Errorf("bucket %s is shown as used, not remaining", b.GetKey())
				}
			}
		})
	}
}

func percentValue(t *testing.T, b *typev1.LimitBucket) uint32 {
	t.Helper()
	p, ok := b.GetPercentUsed().GetKind().(*typev1.Percent_Value)
	if !ok {
		t.Fatalf("bucket %s percent is not a value: %v", b.GetKey(), b.GetPercentUsed())
	}
	return p.Value
}

func resetLocal(t *testing.T, b *typev1.LimitBucket) time.Time {
	t.Helper()
	if b.GetResetsTime() == nil {
		t.Fatalf("bucket %s has no reset", b.GetKey())
	}
	if b.GetResetsTime().GetResolvedWithTimezone() == "" {
		t.Errorf("bucket %s reset names no timezone", b.GetKey())
	}
	return b.GetResetsTime().GetResolvedTime().AsTime().In(time.Local)
}

// The /status capture keeps the usage box on screen above the status
// box, as the real pane showed it; it reads the same buckets.
func TestParseUsageOnStatusScreen(t *testing.T) {
	got := ParseUsage(loadScreen(t, "1.4.7", "status.txt"),
		time.Date(2026, time.October, 12, 20, 50, 0, 0, time.Local))
	if got.ParseOutcome != typev1.ParseOutcome_PARSE_OUTCOME_OK {
		t.Fatalf("outcome = %v, want OK", got.ParseOutcome)
	}
	if p := percentValue(t, got.Buckets[1]); p != 5 {
		t.Errorf("weekly percent = %d, want 5", p)
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

// A bucket row the box does not show keeps its key with a NOT_SHOWN
// percent and the reading is partial, never guessed.
func TestParseUsageMissingWeekly(t *testing.T) {
	screen := dropLines(loadScreen(t, "1.4.7", "usage.txt"), "Weekly ")
	got := ParseUsage(screen, time.Date(2026, time.October, 12, 20, 50, 0, 0, time.Local))

	if got.ParseOutcome != typev1.ParseOutcome_PARSE_OUTCOME_PARTIAL {
		t.Fatalf("outcome = %v, want PARTIAL", got.ParseOutcome)
	}
	if len(got.Buckets) != 2 {
		t.Fatalf("buckets = %d, want 2", len(got.Buckets))
	}
	if r := got.Buckets[1].GetPercentUsed().GetUnknown().GetReason(); r != typev1.UnknownReason_UNKNOWN_REASON_NOT_SHOWN {
		t.Errorf("weekly percent reason = %v, want NOT_SHOWN", r)
	}
}

// The session block rides with the reading in the harness's own terms:
// token counts, turns, subagents and the "as of" stamp of every fixture
// version, so the board shows what the owner would see typing /usage.
func TestParseUsageSessionBlock(t *testing.T) {
	t.Parallel()
	cases := []struct {
		version string
		asOf    string
	}{
		{"1.4.4", "4:47 PM"},
		{"1.4.6", "2:26 PM"},
		{"1.4.7", "8:50 PM"},
	}
	for _, tc := range cases {
		t.Run(tc.version, func(t *testing.T) {
			t.Parallel()
			got := ParseUsage(loadScreen(t, tc.version, "usage.txt"),
				time.Date(2026, time.October, 12, 20, 50, 0, 0, time.Local))
			if got.ParseOutcome != typev1.ParseOutcome_PARSE_OUTCOME_OK {
				t.Fatalf("outcome = %v, want OK", got.ParseOutcome)
			}
			want := map[string]string{
				"Input": "0", "Cached": "0", "Output": "0", "Total": "0",
				"Turns": "0", "Subagents": "none", "as of": tc.asOf,
			}
			seen := map[string]bool{}
			for _, f := range got.Shown {
				if f.GetGroup() != "Session usage" {
					t.Errorf("field %q group = %q, want Session usage", f.GetLabel(), f.GetGroup())
				}
				wantValue, ok := want[f.GetLabel()]
				if !ok {
					t.Errorf("unexpected shown field %q = %q", f.GetLabel(), f.GetValue())
					continue
				}
				if f.GetValue() != wantValue {
					t.Errorf("field %q = %q, want %q", f.GetLabel(), f.GetValue(), wantValue)
				}
				seen[f.GetLabel()] = true
			}
			for label := range want {
				if !seen[label] {
					t.Errorf("shown field %q is missing", label)
				}
			}
		})
	}
}

// A session row the box does not show is left out and the reading is
// partial, never guessed.
func TestParseUsageMissingSessionRow(t *testing.T) {
	t.Parallel()
	screen := dropLines(loadScreen(t, "1.4.7", "usage.txt"), "Subagents")
	got := ParseUsage(screen, time.Date(2026, time.October, 12, 20, 50, 0, 0, time.Local))

	if got.ParseOutcome != typev1.ParseOutcome_PARSE_OUTCOME_PARTIAL {
		t.Fatalf("outcome = %v, want PARTIAL", got.ParseOutcome)
	}
	for _, f := range got.Shown {
		if f.GetLabel() == "Subagents" {
			t.Errorf("Subagents field is present but the box did not show it")
		}
	}
	if len(got.Buckets) != 2 {
		t.Errorf("buckets = %d, want 2", len(got.Buckets))
	}
}

// A session row with no readable value (a truncated capture ending at
// the box border) is left out with a partial outcome: the border is box
// drawing, never a subagent count.
func TestParseUsageTruncatedSessionRow(t *testing.T) {
	t.Parallel()
	screen := strings.Replace(
		loadScreen(t, "1.4.7", "usage.txt"), "Subagents  none", "Subagents", 1)
	got := ParseUsage(screen, time.Date(2026, time.October, 12, 20, 50, 0, 0, time.Local))

	if got.ParseOutcome != typev1.ParseOutcome_PARSE_OUTCOME_PARTIAL {
		t.Fatalf("outcome = %v, want PARTIAL", got.ParseOutcome)
	}
	for _, f := range got.Shown {
		if f.GetLabel() == "Subagents" {
			t.Errorf("Subagents field = %q, want it left out", f.GetValue())
		}
	}
}

// A screen with no usage box fails cleanly: no buckets, no numbers.
func TestParseUsageNoBox(t *testing.T) {
	for name, screen := range map[string]string{
		"garbage":       "hello\n",
		"ready screen":  loadScreen(t, "1.4.7", "startup-ready.txt"),
		"command typed": loadScreen(t, "1.4.7", "usage-command-typed.txt"),
		"oversize":      strings.Repeat("x", 65*1024),
	} {
		got := ParseUsage(screen, time.Date(2026, time.October, 12, 20, 50, 0, 0, time.Local))
		if got.ParseOutcome != typev1.ParseOutcome_PARSE_OUTCOME_FAILED {
			t.Errorf("%s: outcome = %v, want FAILED", name, got.ParseOutcome)
		}
		if len(got.Buckets) != 0 {
			t.Errorf("%s: buckets = %d, want 0", name, len(got.Buckets))
		}
	}
}
