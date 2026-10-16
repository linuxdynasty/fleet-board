package claude

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/harness"
)

// PassiveParserVersion is this screen parser's own version: bump it when
// this parser's behavior changes, next to ParserVersion in usage.go (the
// ready parser keeps its own the same way).
const PassiveParserVersion = "1"

// Passive is one parsed usage banner (01 D3): the weekly-limit bucket the
// banner shows. A banner with an unreadable percent or reset yields
// PARTIAL with that part unknown; a screen with no banner yields FAILED
// with no buckets. It never guesses.
type Passive struct {
	Buckets      []*typev1.LimitBucket
	ParseOutcome typev1.ParseOutcome
}

var (
	// "You've used 96% of your weekly limit · resets 10pm
	// (America/New_York)", as 01 D3 records it. Only this banner shape
	// forms a bucket, so the percentages under "What's contributing"
	// never read as limits.
	bannerHeadRe = regexp.MustCompile(`^You've used (\d+)% of your weekly limit\s*(?:·\s*(.*))?$`)
	// The banner's reset tail: "resets 10pm (America/New_York)", with
	// optional minutes and an optional zone.
	bannerResetRe = regexp.MustCompile(`^resets (\d{1,2})(?::(\d{2}))?([ap]m)(?: \(([^)]+)\))?$`)
)

// ParsePassive reads a Claude Code usage banner out of a screen from a
// pane the agent already observes (01 D3). readTime resolves the
// year-less reset text. The newest banner wins when scrollback holds
// more than one.
func ParsePassive(screen string, readTime time.Time) *Passive {
	p := &Passive{}
	if len(screen) > harness.MaxScreenBytes {
		p.ParseOutcome = typev1.ParseOutcome_PARSE_OUTCOME_FAILED
		return p
	}
	lines := strings.Split(harness.StripControls(screen), "\n")

	seen := false
	complete := true
	var bucket *typev1.LimitBucket
	for _, raw := range lines {
		m := bannerHeadRe.FindStringSubmatch(strings.TrimSpace(raw))
		if m == nil {
			continue
		}
		seen = true
		complete = true
		bucket = &typev1.LimitBucket{
			Key:   "weekly_all",
			Label: "weekly limit",
			Kind:  typev1.WindowKind_WINDOW_KIND_WEEKLY,
		}
		n, err := strconv.Atoi(m[1])
		// The explicit lower bound mirrors usage.go: the banner regex
		// admits digits only, and the check keeps the conversion
		// provably in range.
		if err != nil || n < 0 || n > 100 {
			bucket.PercentUsed = harness.PercentUnknown(typev1.UnknownReason_UNKNOWN_REASON_PARSE_FAILED)
			complete = false
		} else {
			bucket.PercentUsed = harness.PercentValue(uint32(n))
		}
		if tail := strings.TrimSpace(m[2]); tail != "" {
			if r := parseBannerReset(tail, readTime); r != nil {
				bucket.ResetsTime = r
			} else {
				complete = false
			}
		} else {
			complete = false
		}
	}
	if !seen {
		p.ParseOutcome = typev1.ParseOutcome_PARSE_OUTCOME_FAILED
		return p
	}
	p.Buckets = []*typev1.LimitBucket{bucket}
	if complete {
		p.ParseOutcome = typev1.ParseOutcome_PARSE_OUTCOME_OK
	} else {
		p.ParseOutcome = typev1.ParseOutcome_PARSE_OUTCOME_PARTIAL
	}
	return p
}

// parseBannerReset reads one banner reset tail ("resets 10pm
// (America/New_York)"). A name that does not resolve falls back to the
// machine's timezone, as parseReset does (01 D2). An unreadable tail is
// nil: the reset stays unknown instead of guessed.
func parseBannerReset(tail string, readTime time.Time) *typev1.ResetTime {
	m := bannerResetRe.FindStringSubmatch(tail)
	if m == nil {
		return nil
	}
	hour, minute, ok := clockParts(m[1], m[2], m[3])
	if !ok {
		return nil
	}
	return harness.NextTimeOfDay(hour, minute, zoneOrLocal(m[4]), readTime, tail)
}
