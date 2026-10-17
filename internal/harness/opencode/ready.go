package opencode

import (
	"regexp"
	"strings"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/harness"
)

// ReadyParserVersion is this screen parser's own version: bump it when its
// behavior changes (01b, fleet-harness-adapter). Spend and identity parsing
// have their own versions in spend.go and identity.go.
const ReadyParserVersion = "1"

// InputState is what the TUI input line holds: the grey placeholder, styled
// text, or unknown (a plain capture never tells them apart, so the colour,
// not the words, decides).
type InputState string

const (
	InputEmpty   InputState = "empty"
	InputHasText InputState = "has-text"
	InputUnknown InputState = "unknown"
)

// Ready is one parsed OpenCode screen for the READY feature (01 E7):
// whether the idle TUI input box shows, and what its input line holds.
// Input is meaningful only when AtPrompt is true. OpenCode never shows a
// folder-trust screen (01 C3), so there is no trust box to report.
type Ready struct {
	AtPrompt     bool
	Input        InputState
	ParseOutcome typev1.ParseOutcome
}

var (
	// The empty placeholder's grey (38;2;128;128;128) and typed text's
	// near-white (38;2;238;238;238) on the input-content line, pinned by
	// the 1.19.0 ANSI fixtures (testdata README). The SGR opener is part
	// of the match so a "128;" inside another value cannot match.
	greyPlaceholderRe = regexp.MustCompile("38;2;128;128;128m")
	fullIntensityRe   = regexp.MustCompile("38;2;238;238;238m")
)

// ParseReady reads an OpenCode screen for the idle TUI input box: the `┃`
// box with the `Build · model` line under the input row. The box is the
// ready pattern; the input-content row (two rows above the model line)
// decides the input state by its style. A plain capture, an open menu or
// dialog, or any other styled colour stays unknown with a partial read:
// menu and dialog detection are later slices, and a guess would type into
// the wrong state. A screen with no input box fails cleanly. It never
// guesses.
func ParseReady(screen string) *Ready {
	r := &Ready{Input: InputUnknown, ParseOutcome: typev1.ParseOutcome_PARSE_OUTCOME_FAILED}
	if len(screen) > harness.MaxScreenBytes {
		return r
	}
	stripped := strings.Split(harness.StripControls(sgrRe.ReplaceAllString(screen, "")), "\n")
	model := -1
	for i, line := range stripped {
		if modelLineRe.MatchString(line) {
			model = i
		}
	}
	if model < 0 {
		return r
	}
	r.AtPrompt = true
	r.ParseOutcome = typev1.ParseOutcome_PARSE_OUTCOME_PARTIAL
	raw := strings.Split(screen, "\n")
	if len(raw) != len(stripped) || model < 2 || !strings.Contains(stripped[model-2], "┃") {
		return r
	}
	content := raw[model-2]
	grey := greyPlaceholderRe.MatchString(content)
	full := fullIntensityRe.MatchString(content)
	switch {
	case grey && !full:
		r.Input = InputEmpty
		r.ParseOutcome = typev1.ParseOutcome_PARSE_OUTCOME_OK
	case full && !grey:
		r.Input = InputHasText
		r.ParseOutcome = typev1.ParseOutcome_PARSE_OUTCOME_OK
	}
	return r
}
