package muse

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/harness"
)

// Versions are the Muse versions this parser has fixtures for, and
// ParserVersion is its own version: bump it when the parser's behavior
// changes (01b, fleet-harness-adapter).
var Versions = []string{"1.4.4", "1.4.6", "1.4.7"}

const ParserVersion = "2"

// Usage is one parsed /usage box: the plan as shown, the two buckets in
// screen order (Current, then Weekly), and the session block (token
// counts, turns, subagents) with the "as of" stamp as shown fields. A row
// the box did not show keeps its key with a NOT_SHOWN percent (buckets)
// or is left out (shown fields), never a guessed number.
type Usage struct {
	Plan         string
	Buckets      []*typev1.LimitBucket
	Shown        []*typev1.ShownField
	ParseOutcome typev1.ParseOutcome
}

var (
	// "Subscription · Muse Code Power Usage": the plan line. The box's
	// right border ends the capture.
	planRe = regexp.MustCompile(`Subscription ·\s*([^│\n]+)`)
	// "Current        17% used · Resets at 10:24 PM": the short window,
	// its reset a time of day.
	currentRe = regexp.MustCompile(`\bCurrent\s+(\d+)% used · (Resets at (\d{1,2}):(\d{2}) ([AP]M))`)
	// "Weekly         5% used · Resets Oct 18 at 8:00 PM": the weekly
	// budget, its reset year-less.
	weeklyRe = regexp.MustCompile(`\bWeekly\s+(\d+)% used · (Resets ([A-Z][a-z]{2}) (\d{1,2}) at (\d{1,2}):(\d{2}) ([AP]M))`)
)

// row is one bucket's fixed identity and the pattern that reads it.
type row struct {
	key    string
	label  string
	kind   typev1.WindowKind
	re     *regexp.Regexp
	weekly bool
}

var rows = []row{
	{"current", "Current", typev1.WindowKind_WINDOW_KIND_SHORT, currentRe, false},
	{"weekly", "Weekly", typev1.WindowKind_WINDOW_KIND_WEEKLY, weeklyRe, true},
}

// sessionRow is one "Session usage" line carried as shown, in the
// harness's own terms: token counts and turns read as digits, subagents
// as shown ("none" when idle), the "as of" stamp as a time of day. The
// subagent value never crosses the box border: a row with no value is
// unreadable, not a border character.
type sessionRow struct {
	label string
	re    *regexp.Regexp
}

var sessionRows = []sessionRow{
	{"Input", regexp.MustCompile(`\bInput\s+(\d+)`)},
	{"Cached", regexp.MustCompile(`\bCached\s+(\d+)`)},
	{"Output", regexp.MustCompile(`\bOutput\s+(\d+)`)},
	{"Total", regexp.MustCompile(`\bTotal\s+(\d+)`)},
	{"Turns", regexp.MustCompile(`\bTurns\s+(\d+)`)},
	{"Subagents", regexp.MustCompile(`\bSubagents\s+([^\s│]+)`)},
	{"as of", regexp.MustCompile(`\bas of\s+(\d{1,2}:\d{2} [AP]M)`)},
}

// ParseUsage reads a Muse /usage box (01 D2) as of Versions.
// readTime is when the screen was captured; it resolves the year-less
// and time-only reset texts in the machine's timezone. A screen with no
// usage box yields FAILED with no buckets; a box missing a row or a
// reset yields PARTIAL with the missing part unknown. It never guesses.
func ParseUsage(screen string, readTime time.Time) *Usage {
	u := &Usage{}
	if len(screen) > harness.MaxScreenBytes {
		u.ParseOutcome = typev1.ParseOutcome_PARSE_OUTCOME_FAILED
		return u
	}
	screen = harness.StripControls(screen)
	if !strings.Contains(screen, "Session usage") {
		u.ParseOutcome = typev1.ParseOutcome_PARSE_OUTCOME_FAILED
		return u
	}
	if m := planRe.FindStringSubmatch(screen); m != nil {
		u.Plan = strings.TrimSpace(m[1])
	}

	complete := true
	for _, r := range rows {
		b := &typev1.LimitBucket{Key: r.key, Label: r.label, Kind: r.kind}
		u.Buckets = append(u.Buckets, b)
		if !fillRow(b, r, screen, readTime) {
			complete = false
		}
	}
	for _, s := range sessionRows {
		m := s.re.FindStringSubmatch(screen)
		if m == nil {
			complete = false
			continue
		}
		u.Shown = append(u.Shown, &typev1.ShownField{
			Group: "Session usage",
			Label: s.label,
			Value: m[1],
		})
	}
	if complete {
		u.ParseOutcome = typev1.ParseOutcome_PARSE_OUTCOME_OK
	} else {
		u.ParseOutcome = typev1.ParseOutcome_PARSE_OUTCOME_PARTIAL
	}
	return u
}

// fillRow reads one row's percent and reset into its bucket and reports
// whether both were read. A row the box never showed is NOT_SHOWN; a row
// it showed in a shape that does not read is PARSE_FAILED.
func fillRow(b *typev1.LimitBucket, r row, screen string, readTime time.Time) bool {
	m := r.re.FindStringSubmatch(screen)
	if m == nil {
		reason := typev1.UnknownReason_UNKNOWN_REASON_PARSE_FAILED
		if !strings.Contains(screen, r.label) {
			reason = typev1.UnknownReason_UNKNOWN_REASON_NOT_SHOWN
		}
		b.PercentUsed = harness.PercentUnknown(reason)
		return false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n < 0 || n > 100 {
		b.PercentUsed = harness.PercentUnknown(typev1.UnknownReason_UNKNOWN_REASON_PARSE_FAILED)
		return false
	}
	b.PercentUsed = harness.PercentValue(uint32(n))

	var reset *typev1.ResetTime
	if r.weekly {
		reset = weeklyReset(m, readTime)
	} else {
		reset = currentReset(m, readTime)
	}
	if reset == nil {
		return false
	}
	b.ResetsTime = reset
	return true
}

func currentReset(m []string, readTime time.Time) *typev1.ResetTime {
	hour, minute, ok := clock(m[3], m[4], m[5])
	if !ok {
		return nil
	}
	return harness.NextTimeOfDay(hour, minute, time.Local, readTime, m[2])
}

func weeklyReset(m []string, readTime time.Time) *typev1.ResetTime {
	month, ok := harness.MonthFromShort(m[3])
	if !ok {
		return nil
	}
	day, err := strconv.Atoi(m[4])
	if err != nil {
		return nil
	}
	hour, minute, ok := clock(m[5], m[6], m[7])
	if !ok {
		return nil
	}
	return harness.NextMonthDayTime(month, day, hour, minute, time.Local, readTime, m[2])
}

func clock(hour, minute, ampm string) (int, int, bool) {
	h, err := strconv.Atoi(hour)
	if err != nil {
		return 0, 0, false
	}
	mn, err := strconv.Atoi(minute)
	if err != nil {
		return 0, 0, false
	}
	return harness.Clock12(h, mn, ampm == "PM")
}
