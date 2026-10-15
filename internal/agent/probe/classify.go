package probe

import (
	"strings"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/harness"
	"github.com/linuxdynasty/fleet-board/internal/harness/claude"
	"github.com/linuxdynasty/fleet-board/internal/harness/codex"
	"github.com/linuxdynasty/fleet-board/internal/harness/cursor"
	"github.com/linuxdynasty/fleet-board/internal/harness/muse"
)

// Outcome is what one captured probe screen means for the pass. Unknown
// types nothing: the pass reports the pane busy or the read timed out and
// sends health only, so a screen nobody verified never takes a key.
type Outcome int

const (
	// OutcomeUnknown is a screen that is neither trust, a known startup
	// screen nor a proven idle prompt.
	OutcomeUnknown Outcome = iota
	// OutcomeTrust is a folder-trust screen the owner answers by hand.
	OutcomeTrust
	// OutcomeStartup is a startup screen the fleet never answers.
	OutcomeStartup
	// OutcomeReady is a proven idle prompt with an empty input line: the
	// only screen the status command is typed into.
	OutcomeReady
)

// Startup kinds for OutcomeStartup with fixture-proven markers. A blocking
// screen with no proven class (the Codex daemon-settings prompt) carries
// no kind: the caller reports STARTUP_PROMPT_SEEN without one rather than
// guessing. Login markers land with live-probe fixtures (fleet-live-probe),
// never invented.
const (
	KindUpdate       = "update"
	KindReleaseNotes = "release_notes"
)

// Input is what the screen's input line holds, where the screen shows it.
type Input int

const (
	// InputUnknown means the screen shows no input line to read.
	InputUnknown Input = iota
	// InputEmpty means the line is empty (a placeholder counts as empty).
	InputEmpty
	// InputDraft means typed text sits in the line: typing would queue
	// into it, so the pass reports PANE_BUSY and types nothing.
	InputDraft
)

// Classification is one screen read.
type Classification struct {
	Outcome Outcome
	// Kind is the startup class for OutcomeStartup: update, release_notes,
	// or "" when the screen blocks but matches no proven class.
	Kind string
	// Input is the input line state where the screen shows one.
	Input Input
}

// StatusCommand is the one read-only command a probe types for a harness:
// Claude and Muse read /usage, Codex reads /status, Cursor reads /usage.
// OpenCode takes no probe session (usage comes from its stats command in
// another slice), so it and anything unlisted report false and the pass
// refuses without typing.
func StatusCommand(harnessName string) (string, bool) {
	switch harnessName {
	case "claude", "muse", "cursor":
		return "/usage", true
	case "codex":
		return "/status", true
	default:
		return "", false
	}
}

// Classify reads one captured screen (as captured, escapes kept: the
// Claude placeholder check and the Cursor parser need them) for a harness.
// Trust comes from the adapters' own verified ParseReady; startup classes
// come from fixture-proven markers; ready needs the harness's idle proof
// with an empty input line. Checks run refusal-first: trust, then named
// startup screens, then ready, so a prompt screen never reads as idle.
func Classify(harnessName, screen string) Classification {
	if len(screen) > harness.MaxScreenBytes {
		return Classification{Outcome: OutcomeUnknown}
	}
	// Markers read the sequence-stripped screen: live captures keep
	// styling (capture-pane -e), and separately styled words ("[1mClaude[0m
	// [1mCode") would otherwise never match their plain phrase. Trust
	// still goes through the adapters' ParseReady on the raw screen, and
	// the Claude placeholder check needs the raw escapes too.
	clean := harness.StripControls(stripESC(screen))
	switch harnessName {
	case "claude":
		return classifyClaude(screen, clean)
	case "codex":
		return classifyCodex(clean)
	case "muse":
		return classifyMuse(clean)
	case "cursor":
		return classifyCursor(screen)
	default:
		return Classification{Outcome: OutcomeUnknown}
	}
}

// trustBox reports the adapter-verified trust screen: the parse must hold
// OK with the box showing; both the heading and its answer row must show,
// anything else fails cleanly and types nothing.
func trustBox(outcome typev1.ParseOutcome, trust bool) bool {
	return outcome == typev1.ParseOutcome_PARSE_OUTCOME_OK && trust
}

func classifyClaude(raw, stripped string) Classification {
	if r := claude.ParseReady(raw); trustBox(r.ParseOutcome, r.TrustBox) {
		return Classification{Outcome: OutcomeTrust}
	}
	if strings.Contains(stripped, "Release notes") &&
		strings.Contains(stripped, "Select a version to view its notes.") {
		return Classification{Outcome: OutcomeStartup, Kind: KindReleaseNotes}
	}
	if !strings.Contains(stripped, "Claude Code v") {
		return Classification{Outcome: OutcomeUnknown}
	}
	line, ok := inputLine(raw, "❯")
	if !ok {
		return Classification{Outcome: OutcomeUnknown}
	}
	if lineEmpty(line) {
		return Classification{Outcome: OutcomeReady, Input: InputEmpty}
	}
	return Classification{Outcome: OutcomeUnknown, Input: InputDraft}
}

func classifyCodex(stripped string) Classification {
	if r := codex.ParseReady(stripped); trustBox(r.ParseOutcome, r.TrustBox) {
		return Classification{Outcome: OutcomeTrust}
	}
	if strings.Contains(stripped, "Update available") &&
		strings.Contains(stripped, "1. Update now") {
		return Classification{Outcome: OutcomeStartup, Kind: KindUpdate}
	}
	for line := range strings.Lines(stripped) {
		if strings.TrimSpace(line) == "› Ask Codex to do anything" {
			return Classification{Outcome: OutcomeReady, Input: InputEmpty}
		}
	}
	for line := range strings.Lines(stripped) {
		if t := strings.TrimSpace(line); strings.HasPrefix(t, "›") && len(t) > len("›") {
			return Classification{Outcome: OutcomeUnknown, Input: InputDraft}
		}
	}
	return Classification{Outcome: OutcomeUnknown}
}

func classifyMuse(stripped string) Classification {
	if r := muse.ParseReady(stripped); trustBox(r.ParseOutcome, r.TrustBox) {
		return Classification{Outcome: OutcomeTrust}
	}
	if !strings.Contains(stripped, "Muse Code") {
		return Classification{Outcome: OutcomeUnknown}
	}
	for line := range strings.Lines(stripped) {
		if strings.TrimSpace(line) == "❯" {
			return Classification{Outcome: OutcomeReady, Input: InputEmpty}
		}
	}
	for line := range strings.Lines(stripped) {
		if strings.HasPrefix(strings.TrimSpace(line), "❯") {
			return Classification{Outcome: OutcomeUnknown, Input: InputDraft}
		}
	}
	return Classification{Outcome: OutcomeUnknown}
}

func classifyCursor(raw string) Classification {
	if r := cursor.ParseReady(raw); trustBox(r.ParseOutcome, r.TrustBox) {
		return Classification{Outcome: OutcomeTrust}
	}
	r := cursor.ParseReady(raw)
	if r.AtPrompt && r.ParseOutcome == typev1.ParseOutcome_PARSE_OUTCOME_OK && r.Input == cursor.InputEmpty {
		return Classification{Outcome: OutcomeReady, Input: InputEmpty}
	}
	if r.AtPrompt && r.Input == cursor.InputHasText {
		return Classification{Outcome: OutcomeUnknown, Input: InputDraft}
	}
	return Classification{Outcome: OutcomeUnknown}
}

// inputLine finds the input line: the line holding mark (❯) on the raw
// screen, with everything up to and including the mark cut away.
func inputLine(raw, mark string) (string, bool) {
	for line := range strings.Lines(raw) {
		if i := strings.Index(line, mark); i >= 0 {
			return line[i+len(mark):], true
		}
	}
	return "", false
}

// faintRe is the dim attribute a placeholder carries (01b 0.19): typed
// text never does. The SGR opener is required so a "2;" inside a color
// value cannot match.
const faintPrefix = "\x1b["

// lineEmpty reports an input line with no typed text: nothing after the
// mark once escapes come off, or a placeholder, which carries the faint
// attribute over its text.
func lineEmpty(after string) bool {
	plain := stripESC(after)
	if strings.TrimSpace(plain) == "" {
		return true
	}
	for i := 0; i+len(faintPrefix) <= len(after); i++ {
		if after[i:i+len(faintPrefix)] == faintPrefix {
			rest := after[i+len(faintPrefix):]
			if strings.HasPrefix(rest, "2m") || strings.HasPrefix(rest, "0;2m") {
				return true
			}
		}
	}
	return false
}

// stripESC cuts ESC [ ... letter sequences (SGR and friends) out of s.
func stripESC(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && (s[j] < 'A' || s[j] > 'Z') && (s[j] < 'a' || s[j] > 'z') {
				j++
			}
			if j < len(s) {
				j++
			}
			i = j
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}
