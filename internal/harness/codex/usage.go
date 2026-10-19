package codex

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/harness"
)

// Versions are the Codex versions this parser has fixtures for, and
// ParserVersion is its own version: bump it when the parser's behavior
// changes (01b, fleet-harness-adapter).
var Versions = []string{"0.160.0", "0.162.1", "0.163.0"}

const ParserVersion = "1"

// Usage is one parsed /status card: the plan as shown and the weekly
// bucket. Codex shows a percent left; the bucket carries the percent
// used with shown_as_remaining set, and a screen that keeps the row but
// changes its shape yields the bucket unknown, never a guess.
type Usage struct {
	Plan         string
	Buckets      []*typev1.LimitBucket
	ParseOutcome typev1.ParseOutcome
}

var (
	// "Weekly limit:        [░░░░░░░░░░░░░░░░░░░░] 0% left (resets 5:17 AM
	// on 14 Oct)": the one limit row. The bar is optional in the match;
	// only the percent left and the reset text are read.
	weeklyRe = regexp.MustCompile(`Weekly limit:\s*(?:\[[^\]]*\]\s*)?(\d+)% left \(resets (.+)\)`)
	// "5:17 AM on 14 Oct": the year-less reset inside the brackets.
	resetRe = regexp.MustCompile(`^(\d{1,2}):(\d{2}) ([AP]M) on (\d{1,2}) ([A-Z][a-z]{2})$`)
	// "Account:             Pro 200" on 0.160.0; 0.162.1 puts the sign-in
	// email first: "Account: user@example.com (Pro 200)".
	accountRe = regexp.MustCompile(`(?m)^\s*Account:\s*(.+?)\s*$`)
	planInRe  = regexp.MustCompile(`\(([^)]+)\)\s*$`)
)

// ParseUsage reads a Codex /status card (01 D2) as of Versions. readTime
// is when the screen was captured; it resolves the year-less reset text
// in the machine's timezone. A screen with no weekly row yields FAILED
// with no buckets; a row the parser cannot read yields PARTIAL with the
// bucket unknown. It never guesses.
func ParseUsage(screen string, readTime time.Time) *Usage {
	u := &Usage{}
	if len(screen) > harness.MaxScreenBytes {
		u.ParseOutcome = typev1.ParseOutcome_PARSE_OUTCOME_FAILED
		return u
	}
	screen = harness.StripControls(screen)
	if !strings.Contains(screen, "Weekly limit:") {
		u.ParseOutcome = typev1.ParseOutcome_PARSE_OUTCOME_FAILED
		return u
	}

	u.Plan = parsePlan(screen)
	b := &typev1.LimitBucket{
		Key:              "weekly",
		Label:            "Weekly limit",
		Kind:             typev1.WindowKind_WINDOW_KIND_WEEKLY,
		ShownAsRemaining: true,
	}
	u.Buckets = []*typev1.LimitBucket{b}

	m := weeklyRe.FindStringSubmatch(screen)
	if m == nil {
		b.PercentUsed = harness.PercentUnknown(typev1.UnknownReason_UNKNOWN_REASON_PARSE_FAILED)
		u.ParseOutcome = typev1.ParseOutcome_PARSE_OUTCOME_PARTIAL
		return u
	}
	left, err := strconv.Atoi(m[1])
	if err != nil || left < 0 || left > 100 {
		b.PercentUsed = harness.PercentUnknown(typev1.UnknownReason_UNKNOWN_REASON_PARSE_FAILED)
		u.ParseOutcome = typev1.ParseOutcome_PARSE_OUTCOME_PARTIAL
		return u
	}
	b.PercentUsed = harness.PercentValue(uint32(100 - left))
	reset := parseReset(m[2], readTime)
	if reset == nil {
		u.ParseOutcome = typev1.ParseOutcome_PARSE_OUTCOME_PARTIAL
		return u
	}
	b.ResetsTime = reset
	u.ParseOutcome = typev1.ParseOutcome_PARSE_OUTCOME_OK
	return u
}

// parseReset reads "5:17 AM on 14 Oct" in the machine's timezone. The
// card names no zone, so the machine's is the resolver (01 D2).
func parseReset(text string, readTime time.Time) *typev1.ResetTime {
	m := resetRe.FindStringSubmatch(text)
	if m == nil {
		return nil
	}
	hour12, err := strconv.Atoi(m[1])
	if err != nil {
		return nil
	}
	minute, err := strconv.Atoi(m[2])
	if err != nil {
		return nil
	}
	hour, minute, ok := harness.Clock12(hour12, minute, m[3] == "PM")
	if !ok {
		return nil
	}
	day, err := strconv.Atoi(m[4])
	if err != nil {
		return nil
	}
	month, ok := harness.MonthFromShort(m[5])
	if !ok {
		return nil
	}
	return harness.NextMonthDayTime(month, day, hour, minute, time.Local, readTime, "resets "+text)
}

// parsePlan reads the plan off the account line. When the line carries
// the sign-in email, the plan is the bracketed words after it; a line
// that is only an email names no plan, because an email is identity
// (C2), never a plan label.
func parsePlan(screen string) string {
	m := accountRe.FindStringSubmatch(screen)
	if m == nil {
		return ""
	}
	value := strings.TrimSpace(m[1])
	if pm := planInRe.FindStringSubmatch(value); pm != nil {
		return strings.TrimSpace(pm[1])
	}
	if strings.Contains(value, "@") {
		return ""
	}
	return value
}
