package claude

import (
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/harness"
)

// PromptParserVersion is this screen parser's own version: bump it when
// its behavior changes (01b, fleet-harness-adapter). Usage parsing has
// its own ParserVersion in usage.go, READY has ReadyParserVersion.
const PromptParserVersion = "1"

// Prompt is one parsed Claude Code screen for the PROMPT feature (01 E6):
// a permission prompt's text, its options, whether it is destructive,
// how it was found and how sure the match is. Text and option labels
// are untrusted text, capped the way the contract caps them. A failed
// parse carries no prompt content: the screen is shown as a notice only
// (answerable=false) until PROMPT is verified for the version.
type Prompt struct {
	Text              string
	Options           []*typev1.PromptOption
	Destructive       bool
	DestructiveReason string
	Source            typev1.PromptSource
	Confidence        uint32
	Class             typev1.PromptClass
	ParseOutcome      typev1.ParseOutcome
}

// ParsePrompt reads a Claude Code screen for a permission prompt. No
// prompt shape is verified yet: permission fixtures need a live probe
// with model calls (quota), so every screen fails cleanly and prompts
// stay notice-only. It never guesses from question words alone.
func ParsePrompt(screen string) *Prompt {
	p := &Prompt{ParseOutcome: typev1.ParseOutcome_PARSE_OUTCOME_FAILED}
	if len(screen) > harness.MaxScreenBytes {
		return p
	}
	return p
}
