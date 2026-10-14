package cursor

import (
	"regexp"
	"strings"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/harness"
)

// InputState is what the Cursor input line holds: the empty placeholder,
// typed text, or unknown (a plain capture never tells them apart, 01b 0.19).
type InputState string

const (
	InputEmpty   InputState = "empty"
	InputHasText InputState = "has-text"
	InputUnknown InputState = "unknown"
)

// Ready is one parsed Cursor screen: whether the ready prompt shows, whether
// the Workspace Trust box shows instead (01 C3 FOLDER_UNTRUSTED), and what
// the input line holds. Input is meaningful only when AtPrompt is true.
type Ready struct {
	AtPrompt     bool
	TrustBox     bool
	Input        InputState
	ParseOutcome typev1.ParseOutcome
}

var (
	// "Workspace Trust Required" with its "[a] Trust this workspace" row:
	// the trust box. The pane is not at a prompt until the owner answers.
	trustTitleRe = regexp.MustCompile(`Workspace Trust Required`)
	trustRowRe   = regexp.MustCompile(`\[a\] Trust this workspace`)
	// The faint attribute on a styled input line (01b 0.19): the empty
	// placeholder's arrow and text carry it, typed text never does. The
	// pattern needs the SGR opener so a "2;" inside a color value (for
	// example 48;2;21;21;21) cannot match.
	faintRe = regexp.MustCompile("\x1b\\[(?:0;)?2m")
)

// ParseReady reads a Cursor screen for the ready prompt (01b, row 30). The
// input box renders below the transcript, so with several `→` lines the
// styled reading comes from them together: one styled line decides by its
// faint attribute, no styled line leaves the input unknown, and several
// styled lines stay unknown until fixtures pin that shape. A screen with no
// prompt line and no trust box fails cleanly. It never guesses.
func ParseReady(screen string) *Ready {
	r := &Ready{Input: InputUnknown}
	if len(screen) > harness.MaxScreenBytes {
		r.ParseOutcome = typev1.ParseOutcome_PARSE_OUTCOME_FAILED
		return r
	}
	stripped := stripControls(screen)
	if trustTitleRe.FindString(stripped) != "" && trustRowRe.FindString(stripped) != "" {
		r.TrustBox = true
		r.ParseOutcome = typev1.ParseOutcome_PARSE_OUTCOME_OK
		return r
	}
	prompt := 0
	for line := range strings.Lines(stripped) {
		if strings.Contains(line, "→") {
			prompt++
		}
	}
	if prompt == 0 {
		r.ParseOutcome = typev1.ParseOutcome_PARSE_OUTCOME_FAILED
		return r
	}
	r.AtPrompt = true
	// The faint check needs the raw ESC bytes stripControls drops, so
	// re-scan the screen for styled input lines.
	styled, faint := styledInput(screen)
	switch {
	case styled == 1 && faint:
		r.Input = InputEmpty
		r.ParseOutcome = typev1.ParseOutcome_PARSE_OUTCOME_OK
	case styled == 1:
		r.Input = InputHasText
		r.ParseOutcome = typev1.ParseOutcome_PARSE_OUTCOME_OK
	default:
		r.ParseOutcome = typev1.ParseOutcome_PARSE_OUTCOME_PARTIAL
	}
	return r
}

// styledInput re-scans the raw screen for `→` lines carrying ESC bytes and
// reports how many there are and whether any carries the faint attribute.
func styledInput(screen string) (int, bool) {
	styled := 0
	faint := false
	for line := range strings.Lines(screen) {
		if !strings.Contains(line, "→") || !strings.Contains(line, "\x1b") {
			continue
		}
		styled++
		if faintRe.FindString(line) != "" {
			faint = true
		}
	}
	return styled, faint
}
