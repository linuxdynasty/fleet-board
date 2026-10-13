package cursor

import (
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// Version is the cursor-agent version this parser reads, and ParserVersion
// is its own version: bump it when the parser's behavior changes (01b,
// fleet-harness-adapter).
const (
	Version       = "2026.10.01"
	ParserVersion = "1"
)

// maxScreenBytes caps the input at the ShownUsage raw-screen cap: a longer
// screen is not parsed, it fails cleanly.
const maxScreenBytes = 64 * 1024

// Usage is one parsed /usage dialog: the plan as shown, the three monthly
// buckets in screen order (Included, Auto, API), and whether use past the
// plan is billed. A bucket the screen did not show keeps its key with a
// NOT_SHOWN percent, never a guessed number.
type Usage struct {
	Plan           string
	Buckets        []*typev1.LimitBucket
	OverageBilling typev1.OverageBilling
	ParseOutcome   typev1.ParseOutcome
}

var (
	// " Usage • Pro+ ... Resets Nov 4": the dialog title. The plan is the
	// text between the bullet and the reset padding.
	titleRe = regexp.MustCompile(`(?m)^\s*Usage • (.+?)\s{2,}Resets `)
	// "Resets Nov 4": one reset date for the whole plan, no time of day.
	resetRe = regexp.MustCompile(`Resets ([A-Z][a-z]{2}) (\d{1,2})\b`)
	// " Included        100% used   ███…": one bucket row. Auto and API are
	// indented; only "% used" as shown is read, never inferred.
	rowRe = regexp.MustCompile(`(?m)^\s*(Included|Auto|API)\s+(\d+)% used\b`)
	// " On-Demand       Disabled    ───…": the billing row. Only this row
	// decides billing; the "On-demand usage is off" note does not.
	onDemandRe = regexp.MustCompile(`(?m)^\s*On-Demand\s+(Disabled|Enabled)\b`)
)

var bucketKeys = []struct {
	label string
	key   string
}{
	{"Included", "monthly_included"},
	{"Auto", "monthly_auto"},
	{"API", "monthly_api"},
}

var months = map[string]time.Month{
	"Jan": time.January, "Feb": time.February, "Mar": time.March,
	"Apr": time.April, "May": time.May, "Jun": time.June,
	"Jul": time.July, "Aug": time.August, "Sep": time.September,
	"Oct": time.October, "Nov": time.November, "Dec": time.December,
}

// ParseUsage reads a Cursor /usage dialog (01b D2) as of Version. readTime
// is when the screen was captured; it resolves the shown reset date to a
// year. A screen with no usage dialog yields FAILED with no buckets; a
// dialog with a missing row, reset or billing line yields PARTIAL with the
// missing part unknown. It never guesses.
func ParseUsage(screen string, readTime time.Time) *Usage {
	u := &Usage{OverageBilling: typev1.OverageBilling_OVERAGE_BILLING_UNKNOWN}
	if len(screen) > maxScreenBytes {
		u.ParseOutcome = typev1.ParseOutcome_PARSE_OUTCOME_FAILED
		return u
	}
	screen = stripControls(screen)
	title := titleRe.FindStringSubmatch(screen)
	if title == nil {
		u.ParseOutcome = typev1.ParseOutcome_PARSE_OUTCOME_FAILED
		return u
	}
	u.Plan = strings.TrimSpace(title[1])

	seen := map[string]uint32{}
	complete := true
	for _, m := range rowRe.FindAllStringSubmatch(screen, -1) {
		n, err := strconv.Atoi(m[2])
		if err != nil || n < 0 || n > math.MaxUint32 {
			continue
		}
		seen[m[1]] = uint32(n)
	}

	var reset *resolvedReset
	if m := resetRe.FindStringSubmatch(screen); m != nil {
		if month, ok := months[m[1]]; ok {
			if day, err := strconv.Atoi(m[2]); err == nil {
				reset = resolveReset(month, day, readTime, m[0])
			}
		}
	}

	var window *durationpb.Duration
	if reset != nil {
		if w, ok := MonthlyWindow(reset.ShownMonth, reset.ShownDay, reset.ShownYear); ok {
			window = durationpb.New(w)
		}
	}

	switch m := onDemandRe.FindStringSubmatch(screen); {
	case m == nil:
		complete = false
	case m[1] == "Disabled":
		u.OverageBilling = typev1.OverageBilling_OVERAGE_BILLING_OFF
	default:
		u.OverageBilling = typev1.OverageBilling_OVERAGE_BILLING_ON
	}

	for _, b := range bucketKeys {
		bucket := &typev1.LimitBucket{
			Key:              b.key,
			Label:            b.label,
			Kind:             typev1.WindowKind_WINDOW_KIND_MONTHLY,
			Window:           window,
			ShownAsRemaining: false,
		}
		if reset != nil {
			bucket.ResetsTime = reset.ResetTime
		}
		if v, ok := seen[b.label]; ok {
			bucket.PercentUsed = &typev1.Percent{
				Kind: &typev1.Percent_Value{Value: v},
			}
		} else {
			complete = false
			bucket.PercentUsed = &typev1.Percent{
				Kind: &typev1.Percent_Unknown{
					Unknown: &typev1.PercentUnknown{
						Reason: typev1.UnknownReason_UNKNOWN_REASON_NOT_SHOWN,
					},
				},
			}
		}
		u.Buckets = append(u.Buckets, bucket)
	}
	if reset == nil {
		complete = false
	}
	if complete {
		u.ParseOutcome = typev1.ParseOutcome_PARSE_OUTCOME_OK
	} else {
		u.ParseOutcome = typev1.ParseOutcome_PARSE_OUTCOME_PARTIAL
	}
	return u
}

// resolvedReset is a parsed "Resets <Mon> <D>" with its resolved end and
// the shown date's year, which the window rule needs (the end instant can
// fall in the next month: Dec 31 ends Jan 1).
type resolvedReset struct {
	*typev1.ResetTime
	ShownMonth time.Month
	ShownDay   int
	ShownYear  int
}

// resolveReset turns "Resets Nov 4" into the end of that date (00:00 the
// next day) in the machine's timezone: the latest the reset can be, so the
// pace line errs low (03b "Monthly windows"). The year is the next such
// date at or after readTime. A day the month does not have resolves to
// nothing, so the reading stays partial instead of guessing.
func resolveReset(month time.Month, day int, readTime time.Time, shown string) *resolvedReset {
	year := readTime.In(time.Local).Year()
	if day < 1 || day > daysIn(month, year) {
		return nil
	}
	end := time.Date(year, month, day+1, 0, 0, 0, 0, time.Local)
	if !end.After(readTime) {
		year++
		if day > daysIn(month, year) {
			return nil
		}
		end = time.Date(year, month, day+1, 0, 0, 0, 0, time.Local)
	}
	zone, _ := end.Zone()
	if iana := localIANAZone(); iana != "" {
		zone = iana
	}
	return &resolvedReset{
		ResetTime: &typev1.ResetTime{
			ResolvedTime:         timestamppb.New(end),
			OriginalText:         shown,
			ResolvedWithTimezone: zone,
			DateOnly:             true,
		},
		ShownMonth: month,
		ShownDay:   day,
		ShownYear:  year,
	}
}

// MonthlyWindow is the 03b "Monthly windows" rule 2: the window between the
// shown reset and the previous month's same day, set only when the shown
// day D is not the last day of its month and the month before has a day D.
// Months differ in length, so anything else leaves the window unset and the
// bucket paces without one. The diff is calendar days, timezone-free.
func MonthlyWindow(month time.Month, day, year int) (time.Duration, bool) {
	if day < 1 || day >= daysIn(month, year) {
		return 0, false
	}
	prev, prevYear := month-1, year
	if prev < time.January {
		prev, prevYear = time.December, year-1
	}
	if day > daysIn(prev, prevYear) {
		return 0, false
	}
	start := time.Date(prevYear, prev, day, 0, 0, 0, 0, time.UTC)
	end := time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
	return end.Sub(start), true
}

// daysIn reports how many days a month has in a year.
func daysIn(month time.Month, year int) int {
	return time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

// localIANAZone names the machine's timezone ("America/New_York") from the
// system link when it exists; empty when it cannot be told.
func localIANAZone() string {
	target, err := os.Readlink("/etc/localtime")
	if err != nil {
		return ""
	}
	if i := strings.LastIndex(target, "zoneinfo/"); i >= 0 {
		return target[i+len("zoneinfo/"):]
	}
	return ""
}

// stripControls drops ASCII control characters except newline and tab:
// screens are data, and control bytes must not reach the parser.
func stripControls(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, s)
}
