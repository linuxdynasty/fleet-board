package claude

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

// The fixture's zone name is a redaction placeholder (testdata/README.md);
// tests that pin UTC instants swap in a real zone, as that file directs.
func TestParseUsageFixture(t *testing.T) {
	screen := strings.ReplaceAll(loadScreen(t, "2.1.296", "usage.txt"), "Example/Zone", "Etc/UTC")
	readTime := time.Date(2026, time.October, 10, 12, 0, 0, 0, time.UTC)

	got := ParseUsage(screen, readTime)

	if got.ParseOutcome != typev1.ParseOutcome_PARSE_OUTCOME_OK {
		t.Fatalf("outcome = %v, want OK", got.ParseOutcome)
	}
	want := []struct {
		key     string
		label   string
		kind    typev1.WindowKind
		model   string
		percent uint32
		reset   time.Time
		shown   string
	}{
		{
			"session", "Current session", typev1.WindowKind_WINDOW_KIND_SHORT, "", 0,
			time.Date(2026, time.October, 10, 18, 30, 0, 0, time.UTC), "Resets 6:30pm (Etc/UTC)",
		},
		{
			"weekly_all", "Current week (all models)", typev1.WindowKind_WINDOW_KIND_WEEKLY, "", 31,
			time.Date(2026, time.October, 14, 22, 0, 0, 0, time.UTC), "Resets Oct 14 at 10pm (Etc/UTC)",
		},
		{
			"weekly_fable", "Current week (Fable)", typev1.WindowKind_WINDOW_KIND_PER_MODEL, "Fable", 8,
			time.Date(2026, time.October, 14, 22, 0, 0, 0, time.UTC), "Resets Oct 14 at 10pm (Etc/UTC)",
		},
	}
	if len(got.Buckets) != len(want) {
		t.Fatalf("buckets = %d, want %d", len(got.Buckets), len(want))
	}
	for i, w := range want {
		b := got.Buckets[i]
		if b.GetKey() != w.key {
			t.Errorf("bucket %d key = %q, want %q", i, b.GetKey(), w.key)
		}
		if b.GetLabel() != w.label {
			t.Errorf("bucket %s label = %q, want %q", w.key, b.GetLabel(), w.label)
		}
		if b.GetKind() != w.kind {
			t.Errorf("bucket %s kind = %v, want %v", w.key, b.GetKind(), w.kind)
		}
		if b.GetModel() != w.model {
			t.Errorf("bucket %s model = %q, want %q", w.key, b.GetModel(), w.model)
		}
		if b.GetShownAsRemaining() {
			t.Errorf("bucket %s is shown as used, not remaining", w.key)
		}
		p, ok := b.GetPercentUsed().GetKind().(*typev1.Percent_Value)
		if !ok {
			t.Fatalf("bucket %s percent is not a value: %v", w.key, b.GetPercentUsed())
		}
		if p.Value != w.percent {
			t.Errorf("bucket %s percent = %d, want %d", w.key, p.Value, w.percent)
		}
		if b.GetResetsTime() == nil {
			t.Fatalf("bucket %s has no reset", w.key)
		}
		if rt := b.GetResetsTime().GetResolvedTime().AsTime(); !rt.Equal(w.reset) {
			t.Errorf("bucket %s reset = %v, want %v", w.key, rt, w.reset)
		}
		if b.GetResetsTime().GetOriginalText() != w.shown {
			t.Errorf("bucket %s reset text = %q, want %q", w.key, b.GetResetsTime().GetOriginalText(), w.shown)
		}
		if b.GetResetsTime().GetResolvedWithTimezone() != "Etc/UTC" {
			t.Errorf("bucket %s timezone = %q, want Etc/UTC", w.key, b.GetResetsTime().GetResolvedWithTimezone())
		}
		if b.GetWindow() != nil {
			t.Errorf("bucket %s window is set; the screen shows no window length", w.key)
		}
	}
}

// Without the zone swap the printed name does not load, so the reset
// resolves in the machine's timezone at the shown wall-clock time.
func TestParseUsageZoneFallback(t *testing.T) {
	screen := loadScreen(t, "2.1.296", "usage.txt")
	readTime := time.Date(2026, time.October, 10, 12, 0, 0, 0, time.Local)

	got := ParseUsage(screen, readTime)

	if got.ParseOutcome != typev1.ParseOutcome_PARSE_OUTCOME_OK {
		t.Fatalf("outcome = %v, want OK", got.ParseOutcome)
	}
	rt := got.Buckets[1].GetResetsTime().GetResolvedTime().AsTime().In(time.Local)
	y, m, d := rt.Date()
	h, min, _ := rt.Clock()
	if y != 2026 || m != time.October || d != 14 || h != 22 || min != 0 {
		t.Errorf("weekly reset = %v local, want 2026-10-14 22:00 local", rt)
	}
	if got.Buckets[1].GetResetsTime().GetResolvedWithTimezone() == "" {
		t.Errorf("weekly reset names no timezone")
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
	return b.GetPercentUsed().GetUnknown().GetReason()
}

// A core bucket the screen does not show keeps its key with a NOT_SHOWN
// percent and the reading is partial, never guessed.
func TestParseUsageMissingWeekly(t *testing.T) {
	screen := dropLines(loadScreen(t, "2.1.296", "usage.txt"), "Current week (all models)")
	// Dropping the label orphans its percent and reset lines; drop them too,
	// so the screen is one a plan without the weekly row could show. The
	// per-model row shares the reset text, so cut by the bar line instead.
	screen = dropLines(screen, "31% used")
	got := ParseUsage(screen, time.Date(2026, time.October, 10, 12, 0, 0, 0, time.UTC))

	if got.ParseOutcome != typev1.ParseOutcome_PARSE_OUTCOME_PARTIAL {
		t.Fatalf("outcome = %v, want PARTIAL", got.ParseOutcome)
	}
	if len(got.Buckets) != 3 {
		t.Fatalf("buckets = %d, want 3 (session, weekly_all, weekly_fable)", len(got.Buckets))
	}
	if r := percentReason(got.Buckets[1]); r != typev1.UnknownReason_UNKNOWN_REASON_NOT_SHOWN {
		t.Errorf("weekly percent reason = %v, want NOT_SHOWN", r)
	}
}

// A screen with no usage dialog fails cleanly: no buckets, no numbers. The
// "What's contributing" percentages are never read as limits.
func TestParseUsageNoDialog(t *testing.T) {
	for name, screen := range map[string]string{
		"garbage":       "hello\n",
		"ready screen":  loadScreen(t, "2.1.296", "startup-ready.txt"),
		"command typed": loadScreen(t, "2.1.296", "usage-command-typed.txt"),
		"oversize":      strings.Repeat("x", 65*1024),
	} {
		got := ParseUsage(screen, time.Date(2026, time.October, 10, 12, 0, 0, 0, time.UTC))
		if got.ParseOutcome != typev1.ParseOutcome_PARSE_OUTCOME_FAILED {
			t.Errorf("%s: outcome = %v, want FAILED", name, got.ParseOutcome)
		}
		if len(got.Buckets) != 0 {
			t.Errorf("%s: buckets = %d, want 0", name, len(got.Buckets))
		}
	}
}
