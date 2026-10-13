package claude

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/harness"
)

// Versions are the Claude Code versions this parser has fixtures for, and
// ParserVersion is its own version: bump it when the parser's behavior
// changes (01b, fleet-harness-adapter).
var Versions = []string{"2.1.296"}

const ParserVersion = "1"

// Usage is one parsed /usage dialog: the limit buckets in screen order
// (the session and weekly buckets first, then one bucket per per-model
// row shown). A core bucket the screen did not show keeps its key with a
// NOT_SHOWN percent, never a guessed number.
type Usage struct {
	Buckets      []*typev1.LimitBucket
	ParseOutcome typev1.ParseOutcome
}

var (
	// "Current week (Fable)": a limit label on its own line. The percent
	// and its reset follow on the next two lines.
	weekLabelRe = regexp.MustCompile(`^Current week \((.+)\)$`)
	// "31% used", at the end of the bar line under a label. Only the
	// lines under a label are read, so the "What's contributing"
	// percentages below the limits never form a bucket.
	percentRe = regexp.MustCompile(`(\d+)% used\s*$`)
	// "Resets Oct 14 at 10pm (America/New_York)" and "Resets 6:30pm
	// (America/New_York)": a year-less local reset, its timezone named.
	resetMonthDayRe = regexp.MustCompile(`^Resets ([A-Z][a-z]{2}) (\d{1,2}) at (\d{1,2})(?::(\d{2}))?([ap]m)(?: \(([^)]+)\))?$`)
	resetTimeOnlyRe = regexp.MustCompile(`^Resets (\d{1,2})(?::(\d{2}))?([ap]m)(?: \(([^)]+)\))?$`)
)

// label is one limit row's heading as shown, with its bucket identity.
type label struct {
	key   string
	text  string
	kind  typev1.WindowKind
	model string
	core  bool
}

// coreLabels are the two buckets every /usage dialog shows, in order.
var coreLabels = []label{
	{key: "session", text: "Current session", kind: typev1.WindowKind_WINDOW_KIND_SHORT, core: true},
	{key: "weekly_all", text: "Current week (all models)", kind: typev1.WindowKind_WINDOW_KIND_WEEKLY, core: true},
}

// matchLabel reads one line as a limit heading. A "Current week (<model>)"
// line is a per-model bucket; its key slugs the model name as shown
// ("Fable" to "weekly_fable"), fixed across parser versions.
func matchLabel(line string) (label, bool) {
	if line == coreLabels[0].text {
		return coreLabels[0], true
	}
	m := weekLabelRe.FindStringSubmatch(line)
	if m == nil {
		return label{}, false
	}
	if m[1] == "all models" {
		return coreLabels[1], true
	}
	return label{
		key:   "weekly_" + slug(m[1]),
		text:  line,
		kind:  typev1.WindowKind_WINDOW_KIND_PER_MODEL,
		model: m[1],
	}, true
}

func slug(model string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			return r
		default:
			return '_'
		}
	}, model)
}

// ParseUsage reads a Claude Code /usage dialog (01 D2) as of Versions.
// readTime is when the screen was captured; it resolves the year-less
// reset texts. A screen with no limit heading yields FAILED with no
// buckets; a dialog missing a core bucket, a percent or a reset yields
// PARTIAL with the missing part unknown. It never guesses.
func ParseUsage(screen string, readTime time.Time) *Usage {
	u := &Usage{}
	if len(screen) > harness.MaxScreenBytes {
		u.ParseOutcome = typev1.ParseOutcome_PARSE_OUTCOME_FAILED
		return u
	}
	lines := strings.Split(harness.StripControls(screen), "\n")

	type found struct {
		label   label
		percent *uint32
		reset   *typev1.ResetTime
		shown   bool // the heading was on screen
		bad     bool // shown, but its percent or reset did not read
	}
	byKey := map[string]*found{}
	var order []*found
	for _, c := range coreLabels {
		f := &found{label: c}
		byKey[c.key] = f
		order = append(order, f)
	}

	seen := false
	for i, raw := range lines {
		lab, ok := matchLabel(strings.TrimSpace(raw))
		if !ok {
			continue
		}
		seen = true
		f, known := byKey[lab.key]
		if !known {
			f = &found{label: lab}
			byKey[lab.key] = f
			order = append(order, f)
		}
		f.shown = true
		// The percent sits on the line under the heading and its reset
		// on the one after; look at most two lines down, and never past
		// the next heading.
		for j := i + 1; j < len(lines) && j <= i+2; j++ {
			next := strings.TrimSpace(lines[j])
			if _, isLabel := matchLabel(next); isLabel {
				break
			}
			if f.percent == nil {
				if pm := percentRe.FindStringSubmatch(next); pm != nil {
					if n, err := strconv.Atoi(pm[1]); err == nil && n >= 0 && n <= 100 {
						v := uint32(n)
						f.percent = &v
					} else {
						f.bad = true
					}
					continue
				}
			}
			if f.reset == nil && strings.HasPrefix(next, "Resets ") {
				f.reset = parseReset(next, readTime)
				if f.reset == nil {
					f.bad = true
				}
			}
		}
		if f.percent == nil || f.reset == nil {
			f.bad = true
		}
	}

	if !seen {
		u.ParseOutcome = typev1.ParseOutcome_PARSE_OUTCOME_FAILED
		return u
	}

	complete := true
	for _, f := range order {
		b := &typev1.LimitBucket{
			Key:   f.label.key,
			Label: f.label.text,
			Kind:  f.label.kind,
			Model: f.label.model,
		}
		switch {
		case f.percent != nil:
			b.PercentUsed = harness.PercentValue(*f.percent)
		case !f.shown:
			b.PercentUsed = harness.PercentUnknown(typev1.UnknownReason_UNKNOWN_REASON_NOT_SHOWN)
			complete = false
		default:
			b.PercentUsed = harness.PercentUnknown(typev1.UnknownReason_UNKNOWN_REASON_PARSE_FAILED)
			complete = false
		}
		if f.reset != nil {
			b.ResetsTime = f.reset
		} else if f.shown {
			complete = false
		}
		if f.bad {
			complete = false
		}
		u.Buckets = append(u.Buckets, b)
	}
	if complete {
		u.ParseOutcome = typev1.ParseOutcome_PARSE_OUTCOME_OK
	} else {
		u.ParseOutcome = typev1.ParseOutcome_PARSE_OUTCOME_PARTIAL
	}
	return u
}

// parseReset reads one "Resets ..." line. The named timezone resolves the
// text when it loads; a name that does not (a redacted fixture, a zone
// this machine lacks) falls back to the machine's timezone, which the
// spec names as the resolver (01 D2). An unreadable line is nil.
func parseReset(line string, readTime time.Time) *typev1.ResetTime {
	if m := resetMonthDayRe.FindStringSubmatch(line); m != nil {
		month, ok := harness.MonthFromShort(m[1])
		if !ok {
			return nil
		}
		day, err := strconv.Atoi(m[2])
		if err != nil {
			return nil
		}
		hour, minute, ok := clockParts(m[3], m[4], m[5])
		if !ok {
			return nil
		}
		return harness.NextMonthDayTime(month, day, hour, minute, zoneOrLocal(m[6]), readTime, line)
	}
	if m := resetTimeOnlyRe.FindStringSubmatch(line); m != nil {
		hour, minute, ok := clockParts(m[1], m[2], m[3])
		if !ok {
			return nil
		}
		return harness.NextTimeOfDay(hour, minute, zoneOrLocal(m[4]), readTime, line)
	}
	return nil
}

func clockParts(hour, minute, ampm string) (int, int, bool) {
	h, err := strconv.Atoi(hour)
	if err != nil {
		return 0, 0, false
	}
	mn := 0
	if minute != "" {
		mn, err = strconv.Atoi(minute)
		if err != nil {
			return 0, 0, false
		}
	}
	return harness.Clock12(h, mn, ampm == "pm")
}

func zoneOrLocal(name string) *time.Location {
	if name != "" {
		if loc, err := time.LoadLocation(name); err == nil {
			return loc
		}
	}
	return time.Local
}
