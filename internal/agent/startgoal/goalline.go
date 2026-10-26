package startgoal

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/goalfile"
)

// maxGoalLine caps the typed goal line (03c: the 4 KiB cap). The hub
// caps GoalSpec.goal_text at the same size; the consent form and the
// dispatch reason ride inside it.
const maxGoalLine = 4 * 1024

// ErrGoalLine means the line cannot be built: an unknown harness, an
// unknown consent act, or empty goal text. The caller refuses with
// INVALID_ARGUMENT and types nothing.
var ErrGoalLine = errors.New("startgoal: the goal line cannot be built")

// goalHarness reports whether harness has a native /goal the agent can
// type (01b, 01b2): Claude Code, Codex, Muse and Cursor. OpenCode has
// none, so StartGoal there is refused instead of typed blind.
func goalHarness(harness string) bool {
	switch harness {
	case "claude", "codex", "muse", "cursor":
		return true
	default:
		return false
	}
}

// ConsentWord renders one consent act in the harness and provider's
// terms (03c, ADR 0008): the adapter owns the words, so hub text can
// never inject a flag into the typed line.
func ConsentWord(act typev1.ConsentAct) (string, error) {
	switch act {
	case typev1.ConsentAct_CONSENT_ACT_PUSH:
		return "push", nil
	case typev1.ConsentAct_CONSENT_ACT_PR_OPEN:
		return "pr-open", nil
	case typev1.ConsentAct_CONSENT_ACT_MERGE:
		return "merge", nil
	default:
		return "", fmt.Errorf("%w: unknown consent act %s", ErrGoalLine, act)
	}
}

// RenderGoalLine builds the exact line type_goal types (03c "Consent
// form"). With repoShipsOwn (the checkout's goal hooks are the repo's
// own, REPO_DRIVER) the consent rides as flags and the objective
// follows "--"; otherwise the consent rides in the objective text. The
// dispatch reason prefixes the text when the hub names one. Control
// characters are stripped and the line is one line: a hub value can
// never smuggle a newline or a second command into the pane.
func RenderGoalLine(harness string, consent []typev1.ConsentAct, text, reason string, repoShipsOwn bool) (string, error) {
	if !goalHarness(harness) {
		return "", fmt.Errorf("%w: harness %q has no native /goal", ErrGoalLine, harness)
	}
	words := make([]string, 0, len(consent))
	for _, act := range consent {
		w, err := ConsentWord(act)
		if err != nil {
			return "", err
		}
		words = append(words, w)
	}
	body := cleanLine(text)
	if reason != "" {
		body = cleanLine(reason) + " " + body
	}
	body = strings.TrimSpace(body)
	if body == "" {
		return "", fmt.Errorf("%w: empty goal text", ErrGoalLine)
	}
	var line string
	if repoShipsOwn {
		line = "/goal"
		if len(words) > 0 {
			line += " consent " + strings.Join(words, ",")
		}
		line += " -- " + body
	} else {
		line = "/goal " + body
		if len(words) > 0 {
			line += " (consent: " + strings.Join(words, ", ") + ")"
		}
	}
	if !utf8.ValidString(line) || len(line) > maxGoalLine {
		return "", fmt.Errorf("%w: line over the 4 KiB cap", ErrGoalLine)
	}
	return line, nil
}

// cleanLine strips control characters (untrusted hub text) and folds
// whitespace runs to one space, so the line stays one line. Newlines
// fold to spaces first: dropping them outright would glue words together.
func cleanLine(s string) string {
	folded := strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return ' '
		}
		return r
	}, s)
	return strings.Join(strings.Fields(goalfile.Clean(folded, maxGoalLine)), " ")
}
