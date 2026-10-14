package cursor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/pacing"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func loadScreen(t *testing.T, name string) string {
	t.Helper()
	//nolint:gosec // G304: a fixture under the package's own testdata, named by the test, test code only
	raw, err := os.ReadFile(filepath.Join("testdata", "2026.10.01", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return string(raw)
}

// The live /usage capture of 2026-10-13: the account sat at its ceiling, so
// every bucket reads 100. The shape is what the test pins, not the numbers.
func TestParseUsageFixture(t *testing.T) {
	screen := loadScreen(t, "usage.txt")
	readTime := time.Date(2026, time.October, 13, 11, 8, 0, 0, time.UTC)

	got := ParseUsage(screen, readTime)

	if got.ParseOutcome != typev1.ParseOutcome_PARSE_OUTCOME_OK {
		t.Fatalf("outcome = %v, want OK", got.ParseOutcome)
	}
	if got.Plan != "Pro+" {
		t.Errorf("plan = %q, want %q", got.Plan, "Pro+")
	}
	if got.OverageBilling != typev1.OverageBilling_OVERAGE_BILLING_OFF {
		t.Errorf("overage = %v, want OFF", got.OverageBilling)
	}
	wantBuckets := []struct {
		key   string
		label string
		value uint32
	}{
		{"monthly_included", "Included", 100},
		{"monthly_auto", "Auto", 100},
		{"monthly_api", "API", 100},
	}
	if len(got.Buckets) != len(wantBuckets) {
		t.Fatalf("buckets = %d, want %d", len(got.Buckets), len(wantBuckets))
	}
	for i, want := range wantBuckets {
		b := got.Buckets[i]
		if b.GetKey() != want.key {
			t.Errorf("bucket %d key = %q, want %q", i, b.GetKey(), want.key)
		}
		if b.GetLabel() != want.label {
			t.Errorf("bucket %d label = %q, want %q", i, b.GetLabel(), want.label)
		}
		if b.GetKind() != typev1.WindowKind_WINDOW_KIND_MONTHLY {
			t.Errorf("bucket %s kind = %v, want MONTHLY", b.GetKey(), b.GetKind())
		}
		p, ok := b.GetPercentUsed().GetKind().(*typev1.Percent_Value)
		if !ok {
			t.Fatalf("bucket %s percent is not a value: %v", b.GetKey(), b.GetPercentUsed())
		}
		if p.Value != want.value {
			t.Errorf("bucket %s percent = %d, want %d", b.GetKey(), p.Value, want.value)
		}
		if b.GetResetsTime() == nil {
			t.Fatalf("bucket %s has no reset", b.GetKey())
		}
		// "Resets Nov 4" is the end of Nov 4 in the machine's timezone.
		rt := b.GetResetsTime().GetResolvedTime().AsTime()
		y, m, d := rt.In(time.Local).Date()
		h, min, s := rt.In(time.Local).Clock()
		if y != 2026 || m != time.November || d != 5 || h != 0 || min != 0 || s != 0 {
			t.Errorf("bucket %s reset = %v local, want 2026-11-05 00:00 local",
				b.GetKey(), rt.In(time.Local))
		}
		if b.GetResetsTime().GetOriginalText() != "Resets Nov 4" {
			t.Errorf("bucket %s reset text = %q, want %q",
				b.GetKey(), b.GetResetsTime().GetOriginalText(), "Resets Nov 4")
		}
		if !b.GetResetsTime().GetDateOnly() {
			t.Errorf("bucket %s reset is not date_only", b.GetKey())
		}
		if b.GetResetsTime().GetResolvedWithTimezone() == "" {
			t.Errorf("bucket %s reset names no timezone", b.GetKey())
		}
		// Nov 4: not the last day of November, October has a 4th, so the
		// window is Oct 4 to Nov 4: 31 days (03b "Monthly windows" rule 2).
		if b.GetWindow() == nil {
			t.Fatalf("bucket %s has no window", b.GetKey())
		}
		if gotHours := b.GetWindow().AsDuration(); gotHours != 31*24*time.Hour {
			t.Errorf("bucket %s window = %v, want 744h", b.GetKey(), gotHours)
		}
	}
}

// The 03b "Monthly windows" rule 2 table: the window is set only when the
// shown day D is not the last day of its month and the month before has a
// day D. 2026 is a common year, 2024 a leap year.
func TestMonthlyWindowRule(t *testing.T) {
	cases := []struct {
		name  string
		month time.Month
		day   int
		year  int
		want  time.Duration
		ok    bool
	}{
		{"Nov 4", time.November, 4, 2026, 31 * 24 * time.Hour, true},
		{"Mar 4 common", time.March, 4, 2026, 28 * 24 * time.Hour, true},
		{"Mar 4 leap", time.March, 4, 2024, 29 * 24 * time.Hour, true},
		{"Mar 15", time.March, 15, 2026, 28 * 24 * time.Hour, true},
		{"Mar 28", time.March, 28, 2026, 28 * 24 * time.Hour, true},
		{"Feb 28 common is the last day", time.February, 28, 2026, 0, false},
		{"Feb 28 leap", time.February, 28, 2024, 31 * 24 * time.Hour, true},
		{"Mar 29 leap", time.March, 29, 2024, 29 * 24 * time.Hour, true},
		{"Mar 29 common has no Feb 29", time.March, 29, 2026, 0, false},
		{"Apr 30 is the last day", time.April, 30, 2026, 0, false},
		{"Mar 31 is the last day", time.March, 31, 2026, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := MonthlyWindow(tc.month, tc.day, tc.year)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}
			if ok && got != tc.want {
				t.Errorf("window = %v, want %v", got, tc.want)
			}
		})
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

func percentReason(b *typev1.LimitBucket) typev1.UnknownReason {
	// Getters are nil-safe: a value percent yields UNSPECIFIED.
	return b.GetPercentUsed().GetUnknown().GetReason()
}

// A row the screen should show and does not is NOT_SHOWN, and the reading
// is partial, never guessed (03b "Monthly windows").
func TestParseUsageMissingRow(t *testing.T) {
	screen := dropLines(loadScreen(t, "usage.txt"), "API           100%")
	got := ParseUsage(screen, time.Date(2026, time.October, 13, 11, 8, 0, 0, time.UTC))

	if got.ParseOutcome != typev1.ParseOutcome_PARSE_OUTCOME_PARTIAL {
		t.Fatalf("outcome = %v, want PARTIAL", got.ParseOutcome)
	}
	if len(got.Buckets) != 3 {
		t.Fatalf("buckets = %d, want 3", len(got.Buckets))
	}
	if r := percentReason(got.Buckets[2]); r != typev1.UnknownReason_UNKNOWN_REASON_NOT_SHOWN {
		t.Errorf("API percent reason = %v, want NOT_SHOWN", r)
	}
}

// A missing On-Demand row leaves billing unknown, never off (03b step 6
// treats it like on). The "On-demand usage is off" note alone does not
// count: only the table row decides.
func TestParseUsageMissingOnDemand(t *testing.T) {
	screen := dropLines(loadScreen(t, "usage.txt"), "On-Demand       Disabled")
	got := ParseUsage(screen, time.Date(2026, time.October, 13, 11, 8, 0, 0, time.UTC))

	if got.ParseOutcome != typev1.ParseOutcome_PARSE_OUTCOME_PARTIAL {
		t.Fatalf("outcome = %v, want PARTIAL", got.ParseOutcome)
	}
	if got.OverageBilling != typev1.OverageBilling_OVERAGE_BILLING_UNKNOWN {
		t.Errorf("overage = %v, want UNKNOWN", got.OverageBilling)
	}
}

// A screen with no usage dialog fails cleanly: no buckets, no numbers.
func TestParseUsageNoDialog(t *testing.T) {
	for name, screen := range map[string]string{
		"garbage":      "hello\n",
		"ready screen": loadScreen(t, "ready-trusted-child.txt"),
	} {
		got := ParseUsage(screen, time.Date(2026, time.October, 13, 11, 8, 0, 0, time.UTC))
		if got.ParseOutcome != typev1.ParseOutcome_PARSE_OUTCOME_FAILED {
			t.Errorf("%s: outcome = %v, want FAILED", name, got.ParseOutcome)
		}
		if len(got.Buckets) != 0 {
			t.Errorf("%s: buckets = %d, want 0", name, len(got.Buckets))
		}
	}
}

// On-Demand on, or no On-Demand row at all, keeps the account out of
// auto-dispatch (03b step 6, row 30): the reading parses to ON or UNKNOWN
// billing, and pacing excludes it. No live on-state dialog has been
// captured (an open probe, 01b), so the on-case mutates the live capture
// in-test, the same way dropLines mutates it for the missing cases: only
// the table cell changes, and the "On-demand usage is off" note is kept
// verbatim because only the row decides billing.
func TestParseUsageOnDemandExcluded(t *testing.T) {
	read := time.Date(2026, time.October, 13, 11, 8, 0, 0, time.UTC)
	cases := []struct {
		name    string
		screen  func() string
		billing typev1.OverageBilling
	}{
		{
			name: "on demand on",
			screen: func() string {
				return strings.Replace(
					loadScreen(t, "usage.txt"), "On-Demand       Disabled", "On-Demand       Enabled", 1)
			},
			billing: typev1.OverageBilling_OVERAGE_BILLING_ON,
		},
		{
			name: "no on demand row",
			screen: func() string {
				return dropLines(loadScreen(t, "usage.txt"), "On-Demand       Disabled")
			},
			billing: typev1.OverageBilling_OVERAGE_BILLING_UNKNOWN,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// No t.Parallel: the screen loaders report through the
			// parent t, which must stay on this goroutine.
			got := ParseUsage(tc.screen(), read)
			if got.OverageBilling != tc.billing {
				t.Fatalf("overage = %v, want %v", got.OverageBilling, tc.billing)
			}
			in := pacing.Input{
				Usage: &hubv1.AccountUsage{
					ReadTime:       timestamppb.New(read),
					Parse:          got.ParseOutcome,
					Buckets:        got.Buckets,
					OverageBilling: got.OverageBilling,
				},
				Overage: got.OverageBilling,
			}
			res := pacing.Evaluate(read, in)
			if res.Pacing.GetState() != hubv1.PacingState_PACING_STATE_EXCLUDED {
				t.Errorf("state = %v, want EXCLUDED", res.Pacing.GetState())
			}
		})
	}
}
