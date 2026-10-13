package opencode

import (
	"regexp"
	"strconv"
	"strings"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/harness"
)

// Version is the newest OpenCode version this parser reads, and
// ParserVersion is its own version: bump it when the parser's behavior
// changes (01b, fleet-harness-adapter).
const (
	Version       = "1.19.0"
	ParserVersion = "1"
)

// Spend is one parsed `opencode stats` table (01b D2): the all-time counts
// as shown, and the total cost as exact micro-units. Token counts stay text:
// "29.5K" names no base, so turning it into a number would be a guess.
// OpenCode shows no per-window limit and no reset, so there are no buckets.
type Spend struct {
	Sessions               int64
	Messages               int64
	Days                   int64
	TotalMicros            int64
	HasTotal               bool
	Currency               string
	AvgPerDayMicros        int64
	HasAvgPerDay           bool
	AvgTokensPerSession    string
	MedianTokensPerSession string
	InputTokens            string
	OutputTokens           string
	CacheRead              string
	CacheWrite             string
	ParseOutcome           typev1.ParseOutcome
}

var (
	// "│Sessions                                              1 │": one
	// OVERVIEW row. Rows sit inside a box table, so the label (not the
	// line start) anchors the match; separators are blanks, never newlines,
	// so a wrapped line cannot form a false row. Only these three labels
	// exist in the table.
	overviewRe = regexp.MustCompile(`(?m)(Sessions|Messages|Days)[ \t]+(\d+)[ \t]*│`)
	// "│Total Cost                                        $0.00 │": the
	// all-time total. `$` is USD; cents are exactly two digits.
	totalRe = regexp.MustCompile(`(?m)Total Cost[ \t]+(-?\$[\d,]+\.\d{2})[ \t]*│`)
	// "│Avg Cost/Day                                      $0.00 │".
	avgCostRe = regexp.MustCompile(`(?m)Avg Cost/Day[ \t]+(-?\$[\d,]+\.\d{2})[ \t]*│`)
	// "│Input                                             13.0K │": one token
	// row, kept as shown (see Spend).
	tokensRe = regexp.MustCompile(`(?m)(Avg Tokens/Session|Median Tokens/Session|Input|Output|Cache Read|Cache Write)[ \t]+(\S+)[ \t]*│`)
)

// ParseSpend reads an `opencode stats` table (01b D2) as of Version. A
// screen with no stats table yields FAILED with no numbers; a table missing
// its Total Cost row yields PARTIAL with the counts it shows; anything else
// missing yields PARTIAL too. It never guesses.
func ParseSpend(screen string) *Spend {
	s := &Spend{}
	if len(screen) > harness.MaxScreenBytes {
		s.ParseOutcome = typev1.ParseOutcome_PARSE_OUTCOME_FAILED
		return s
	}
	screen = harness.StripControls(screen)

	complete := true
	seenOverview := false
	for _, m := range overviewRe.FindAllStringSubmatch(screen, -1) {
		n, err := strconv.ParseInt(m[2], 10, 64)
		if err != nil {
			continue
		}
		seenOverview = true
		switch m[1] {
		case "Sessions":
			s.Sessions = n
		case "Messages":
			s.Messages = n
		case "Days":
			s.Days = n
		}
	}
	// The three OVERVIEW rows are always shown together; a table with none
	// of them is not a stats table.
	if !seenOverview && totalRe.FindString(screen) == "" {
		s.ParseOutcome = typev1.ParseOutcome_PARSE_OUTCOME_FAILED
		return s
	}

	if m := totalRe.FindStringSubmatch(screen); m != nil {
		micros, ok := dollarsToMicros(m[1])
		if ok {
			s.TotalMicros, s.Currency, s.HasTotal = micros, "USD", true
		} else {
			complete = false
		}
	} else {
		complete = false
	}
	if m := avgCostRe.FindStringSubmatch(screen); m != nil {
		if micros, ok := dollarsToMicros(m[1]); ok {
			s.AvgPerDayMicros, s.HasAvgPerDay = micros, true
		} else {
			complete = false
		}
	} else {
		complete = false
	}
	seen := map[string]string{}
	for _, m := range tokensRe.FindAllStringSubmatch(screen, -1) {
		seen[m[1]] = m[2]
	}
	get := func(label string) string {
		v, ok := seen[label]
		if !ok {
			complete = false
		}
		return v
	}
	s.AvgTokensPerSession = get("Avg Tokens/Session")
	s.MedianTokensPerSession = get("Median Tokens/Session")
	s.InputTokens = get("Input")
	s.OutputTokens = get("Output")
	s.CacheRead = get("Cache Read")
	s.CacheWrite = get("Cache Write")

	if complete {
		s.ParseOutcome = typev1.ParseOutcome_PARSE_OUTCOME_OK
	} else {
		s.ParseOutcome = typev1.ParseOutcome_PARSE_OUTCOME_PARTIAL
	}
	return s
}

// dollarsToMicros turns "$1,234.56" into micro-units with integer math, so
// money is never rounded through a float. Only the two-decimal shape the
// stats table prints parses; anything else is not money.
func dollarsToMicros(text string) (int64, bool) {
	neg := false
	t := strings.TrimPrefix(text, "-")
	if len(t) != len(text) {
		neg = true
	}
	t = strings.TrimPrefix(t, "$")
	dollars, cents, ok := strings.Cut(t, ".")
	if !ok || len(cents) != 2 {
		return 0, false
	}
	dollars = strings.ReplaceAll(dollars, ",", "")
	d, err := strconv.ParseInt(dollars, 10, 64)
	if err != nil {
		return 0, false
	}
	// Untrusted text: refuse totals that do not fit in micro-units rather
	// than wrap them into a wrong number.
	const maxDollars = int64(1<<63-1) / 1000000
	if d > maxDollars {
		return 0, false
	}
	c, err := strconv.ParseInt(cents, 10, 64)
	if err != nil {
		return 0, false
	}
	micros := d*1000000 + c*10000
	if neg {
		micros = -micros
	}
	return micros, true
}
