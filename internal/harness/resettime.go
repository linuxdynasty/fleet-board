package harness

import (
	"os"
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// MaxScreenBytes caps a screen a parser will read, at the ShownUsage
// raw-screen cap: a longer screen is not parsed, it fails cleanly.
const MaxScreenBytes = 64 * 1024

// StripControls drops ASCII control characters except newline and tab:
// screens are untrusted data, and control bytes must not reach a parser
// (01 principle: untrusted text is capped and control-stripped at the
// boundary). ANSI escapes start with ESC, so stripping controls leaves
// their parameter bytes as plain text; usage parsers anchor on labels,
// never on styling, so that text cannot form a false reading.
func StripControls(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, s)
}

var months = map[string]time.Month{
	"Jan": time.January, "Feb": time.February, "Mar": time.March,
	"Apr": time.April, "May": time.May, "Jun": time.June,
	"Jul": time.July, "Aug": time.August, "Sep": time.September,
	"Oct": time.October, "Nov": time.November, "Dec": time.December,
}

// MonthFromShort reads a three-letter month name as the harnesses print it
// ("Oct"). Anything else is not a month, never a guess.
func MonthFromShort(name string) (time.Month, bool) {
	m, ok := months[name]
	return m, ok
}

// Clock12 turns a 12-hour clock reading ("6:30pm") into a 24-hour hour and
// minute. An hour outside 1 to 12 or a minute outside 0 to 59 is not a
// clock reading.
func Clock12(hour, minute int, pm bool) (int, int, bool) {
	if hour < 1 || hour > 12 || minute < 0 || minute > 59 {
		return 0, 0, false
	}
	h := hour % 12
	if pm {
		h += 12
	}
	return h, minute, true
}

// ZoneName names the timezone a reset was resolved with, for
// ResetTime.resolved_with_timezone: the IANA name when it can be told
// ("America/New_York", "Etc/UTC"), else the zone abbreviation.
func ZoneName(loc *time.Location) string {
	if loc == time.Local {
		if iana := localIANAZone(); iana != "" {
			return iana
		}
	}
	return loc.String()
}

// localIANAZone names the machine's timezone from the system link when it
// exists; empty when it cannot be told.
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

// NextMonthDayTime resolves a year-less reset ("Oct 14 at 10pm") to the
// next such date and time strictly after readTime, in loc (01 D2: reset
// texts have no year and are local; the agent ships both the text and
// UTC). shown is the reset text exactly as the harness printed it. A day
// no nearby year has (Feb 30) resolves to nil: the reset stays unknown
// instead of guessed. Feb 29 searches up to four years ahead.
func NextMonthDayTime(month time.Month, day, hour, minute int, loc *time.Location, readTime time.Time, shown string) *typev1.ResetTime {
	if loc == nil || day < 1 || day > 31 || hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return nil
	}
	year := readTime.In(loc).Year()
	for add := range 5 {
		end := time.Date(year+add, month, day, hour, minute, 0, 0, loc)
		if end.Month() != month || end.Day() != day {
			continue // time.Date rolled an impossible day into next month.
		}
		if end.After(readTime) {
			return &typev1.ResetTime{
				ResolvedTime:         timestamppb.New(end),
				OriginalText:         shown,
				ResolvedWithTimezone: ZoneName(loc),
			}
		}
	}
	return nil
}

// NextTimeOfDay resolves a time-only reset ("Resets 6:30pm") to that time
// today in loc, or tomorrow when it has already passed at readTime: a
// reset in the past cannot be the one the screen means.
func NextTimeOfDay(hour, minute int, loc *time.Location, readTime time.Time, shown string) *typev1.ResetTime {
	if loc == nil || hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return nil
	}
	local := readTime.In(loc)
	end := time.Date(local.Year(), local.Month(), local.Day(), hour, minute, 0, 0, loc)
	if !end.After(readTime) {
		end = time.Date(local.Year(), local.Month(), local.Day()+1, hour, minute, 0, 0, loc)
	}
	return &typev1.ResetTime{
		ResolvedTime:         timestamppb.New(end),
		OriginalText:         shown,
		ResolvedWithTimezone: ZoneName(loc),
	}
}

// PercentValue is a percent the screen showed.
func PercentValue(v uint32) *typev1.Percent {
	return &typev1.Percent{Kind: &typev1.Percent_Value{Value: v}}
}

// PercentUnknown is a percent that could not be read, with the reason:
// NOT_SHOWN when the harness did not show the value at all, PARSE_FAILED
// when it showed something the parser could not read. Never a number.
func PercentUnknown(reason typev1.UnknownReason) *typev1.Percent {
	return &typev1.Percent{Kind: &typev1.Percent_Unknown{
		Unknown: &typev1.PercentUnknown{Reason: reason},
	}}
}
