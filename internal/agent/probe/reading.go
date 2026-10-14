package probe

import (
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/harness"
	"github.com/linuxdynasty/fleet-board/internal/harness/claude"
	"github.com/linuxdynasty/fleet-board/internal/harness/codex"
	"github.com/linuxdynasty/fleet-board/internal/harness/cursor"
	"github.com/linuxdynasty/fleet-board/internal/harness/muse"
	"github.com/linuxdynasty/fleet-board/internal/scrub"
)

// BuildReading assembles one UsageReading from a captured usage screen.
// It returns false only for a harness with no usage parser, which the
// pass refuses before it ever types. A screen with no usage content still
// returns a reading with PARSE_FAILED and the redacted raw screen (03b):
// the caller, not this function, decides whether the screen was a usage
// screen at all, by re-classifying it first.
func BuildReading(harnessName, screen string, readTime time.Time, pane *typev1.PaneKey, probeID *typev1.ProbeId, accountKey *typev1.AccountKey, harnessMsg *typev1.Harness) (*agentv1.UsageReading, bool) {
	parsed, ok := parseUsage(harnessName, screen, readTime)
	if !ok {
		return nil, false
	}
	r := &agentv1.UsageReading{
		ProbeId:      probeID,
		AccountKey:   accountKey,
		Harness:      harnessMsg,
		ReadTime:     timestamppb.New(readTime),
		Source:       typev1.UsageSource_USAGE_SOURCE_PROBE,
		Pane:         pane,
		Buckets:      parsed.buckets,
		Parse:        parsed.outcome,
		ParseMessage: parsed.diag,
		Shown: &typev1.ShownUsage{
			RawScreen:    scrub.Text(harness.StripControls(screen), scrub.Options{MaxBytes: 64 * 1024}),
			RawTruncated: len(screen) > 64*1024,
		},
	}
	r.HarnessVersion = ""
	r.ParserVersion = parsed.parserVersion
	if parsed.plan != "" {
		// The plan line can carry the sign-in email (Codex 0.162.1
		// shows "Account: user@example.com (Pro 200)"), so it is
		// scrubbed like the raw screen (01 C2).
		r.Shown.Fields = []*typev1.ShownField{{
			Group: "plan",
			Label: "Plan",
			Value: scrub.Text(parsed.plan, scrub.Options{MaxBytes: 1024}),
		}}
	}
	if parsed.overage != typev1.OverageBilling_OVERAGE_BILLING_UNSPECIFIED {
		r.OverageBilling = parsed.overage
	}
	return r, true
}

// parsedUsage is one parser's normalized output.
type parsedUsage struct {
	buckets       []*typev1.LimitBucket
	plan          string
	overage       typev1.OverageBilling
	outcome       typev1.ParseOutcome
	parserVersion string
	diag          string
}

// parseUsage dispatches to the harness's version-pinned usage parser. The
// parsers never guess: a screen with no usage content yields FAILED with
// no buckets.
func parseUsage(harnessName, screen string, readTime time.Time) (parsedUsage, bool) {
	switch harnessName {
	case "claude":
		u := claude.ParseUsage(screen, readTime)
		return parsedUsage{
			buckets: u.Buckets, outcome: u.ParseOutcome,
			parserVersion: claude.ParserVersion, diag: diag("claude", u.ParseOutcome),
		}, true
	case "codex":
		u := codex.ParseUsage(screen, readTime)
		return parsedUsage{
			buckets: u.Buckets, plan: u.Plan, outcome: u.ParseOutcome,
			parserVersion: codex.ParserVersion, diag: diag("codex", u.ParseOutcome),
		}, true
	case "muse":
		u := muse.ParseUsage(screen, readTime)
		return parsedUsage{
			buckets: u.Buckets, plan: u.Plan, outcome: u.ParseOutcome,
			parserVersion: muse.ParserVersion, diag: diag("muse", u.ParseOutcome),
		}, true
	case "cursor":
		u := cursor.ParseUsage(screen, readTime)
		return parsedUsage{
			buckets: u.Buckets, plan: u.Plan, overage: u.OverageBilling,
			outcome: u.ParseOutcome, parserVersion: cursor.ParserVersion,
			diag: diag("cursor", u.ParseOutcome),
		}, true
	default:
		return parsedUsage{}, false
	}
}

// diag is the parser diagnostic: names and outcome only, never screen
// text (usage.proto caps it at 1 KiB; these are bytes).
func diag(harnessName string, outcome typev1.ParseOutcome) string {
	switch outcome {
	case typev1.ParseOutcome_PARSE_OUTCOME_OK:
		return harnessName + " parser: ok"
	case typev1.ParseOutcome_PARSE_OUTCOME_PARTIAL:
		return harnessName + " parser: partial"
	default:
		return harnessName + " parser: failed"
	}
}
