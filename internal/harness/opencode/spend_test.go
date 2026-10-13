package opencode

import (
	"strings"
	"testing"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// The 1.19.0 stats capture: one session, $0.00 total, token counts with a K
// suffix. Token counts stay as shown (no K-base guess); money is exact.
func TestParseSpendFixture(t *testing.T) {
	got := ParseSpend(readFixture(t, "1.19.0", "stats.txt"))

	if got.ParseOutcome != typev1.ParseOutcome_PARSE_OUTCOME_OK {
		t.Fatalf("outcome = %v, want OK", got.ParseOutcome)
	}
	if got.Sessions != 1 || got.Messages != 3 || got.Days != 1 {
		t.Errorf("overview = %d/%d/%d, want 1/3/1", got.Sessions, got.Messages, got.Days)
	}
	if got.TotalMicros != 0 || got.Currency != "USD" {
		t.Errorf("total = %d %q, want 0 %q", got.TotalMicros, got.Currency, "USD")
	}
	if got.AvgPerDayMicros != 0 {
		t.Errorf("avg per day = %d, want 0", got.AvgPerDayMicros)
	}
	wantTokens := map[string]string{
		"avg per session":    "29.5K",
		"median per session": "29.5K",
		"input":              "13.0K",
		"output":             "112",
		"cache read":         "16.3K",
		"cache write":        "0",
	}
	for name, want := range map[string]string{
		"avg per session":    got.AvgTokensPerSession,
		"median per session": got.MedianTokensPerSession,
		"input":              got.InputTokens,
		"output":             got.OutputTokens,
		"cache read":         got.CacheRead,
		"cache write":        got.CacheWrite,
	} {
		if want != wantTokens[name] {
			t.Errorf("tokens %s = %q, want %q", name, want, wantTokens[name])
		}
	}
}

// The 1.18.34 stats capture: an empty account, every count zero.
func TestParseSpendEmpty(t *testing.T) {
	got := ParseSpend(readFixture(t, "1.18.34", "stats.txt"))

	if got.ParseOutcome != typev1.ParseOutcome_PARSE_OUTCOME_OK {
		t.Fatalf("outcome = %v, want OK", got.ParseOutcome)
	}
	if got.Sessions != 0 || got.Messages != 0 || got.Days != 0 {
		t.Errorf("overview = %d/%d/%d, want 0/0/0", got.Sessions, got.Messages, got.Days)
	}
	if got.TotalMicros != 0 || got.Currency != "USD" {
		t.Errorf("total = %d %q, want 0 %q", got.TotalMicros, got.Currency, "USD")
	}
	if got.InputTokens != "0" || got.OutputTokens != "0" {
		t.Errorf("tokens = %q/%q, want 0/0", got.InputTokens, got.OutputTokens)
	}
}

// A stats table missing its Total Cost row is partial: the counts it shows
// are kept, the total is not invented.
func TestParseSpendMissingTotal(t *testing.T) {
	screen := dropContaining(readFixture(t, "1.19.0", "stats.txt"), "Total Cost")
	got := ParseSpend(screen)

	if got.ParseOutcome != typev1.ParseOutcome_PARSE_OUTCOME_PARTIAL {
		t.Fatalf("outcome = %v, want PARTIAL", got.ParseOutcome)
	}
	if got.Sessions != 1 {
		t.Errorf("sessions = %d, want 1", got.Sessions)
	}
	if got.HasTotal {
		t.Errorf("total reported without a Total Cost row")
	}
}

// A screen with no stats table fails cleanly: no counts, no money.
func TestParseSpendNoTable(t *testing.T) {
	for name, screen := range map[string]string{
		"garbage":       "hello\n",
		"status dialog": readFixture(t, "1.19.0", "tui-status-dialog.txt"),
		"providers":     readFixture(t, "1.19.0", "providers-list.txt"),
	} {
		got := ParseSpend(screen)
		if got.ParseOutcome != typev1.ParseOutcome_PARSE_OUTCOME_FAILED {
			t.Errorf("%s: outcome = %v, want FAILED", name, got.ParseOutcome)
		}
		if got.HasTotal || got.Sessions != 0 {
			t.Errorf("%s: parsed numbers from a screen with no stats table", name)
		}
	}
}

// Money is decimal-exact: $1,234.56 is 1234560000 micros, never float math.
func TestParseSpendMoneyExact(t *testing.T) {
	screen := strings.Replace(
		readFixture(t, "1.19.0", "stats.txt"), "$0.00", "$1,234.56", 1)
	got := ParseSpend(screen)

	if got.ParseOutcome != typev1.ParseOutcome_PARSE_OUTCOME_OK {
		t.Fatalf("outcome = %v, want OK", got.ParseOutcome)
	}
	if !got.HasTotal || got.TotalMicros != 1234560000 {
		t.Errorf("total = %d (has=%v), want 1234560000", got.TotalMicros, got.HasTotal)
	}
}

// A total that does not fit in micro-units is partial, never a wrapped
// number.
func TestParseSpendHugeTotal(t *testing.T) {
	screen := strings.Replace(
		readFixture(t, "1.19.0", "stats.txt"), "$0.00", "$99999999999999999.99", 1)
	got := ParseSpend(screen)

	if got.ParseOutcome != typev1.ParseOutcome_PARSE_OUTCOME_PARTIAL {
		t.Fatalf("outcome = %v, want PARTIAL", got.ParseOutcome)
	}
	if got.HasTotal {
		t.Errorf("total reported for an overflowing amount")
	}
}

func dropContaining(screen, want string) string {
	var kept []string
	for line := range strings.Lines(strings.TrimSuffix(screen, "\n")) {
		if !strings.Contains(line, want) {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n") + "\n"
}
