package opencode

import (
	"regexp"
	"strconv"
	"strings"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/harness"
)

// Provider is one account the harness shows: its name and the rest of its
// row as shown (an env var name, a login type). Both are untrusted text.
// The parser never opens the harness's credential store (01b C2).
type Provider struct {
	Name   string
	Detail string
}

// Providers is one parsed `opencode providers list` output (01b C2): the
// credential count with its rows, and the Environment section when the
// machine shows one. Env rows name only the variable (its value is never
// printed), and a missing Environment section means no env providers, not
// zero parsed badly.
type Providers struct {
	Credentials         int
	CredentialProviders []Provider
	EnvSeen             bool
	EnvCount            int
	EnvProviders        []Provider
	ParseOutcome        typev1.ParseOutcome
}

var (
	// sgrRe strips SGR color sequences from `-e` pane captures before
	// control stripping: the model line and provider rows anchor on box
	// edges and words, and a color code inside either would pollute the
	// captured name. Only SGR (colors) is stripped; other escapes fall to
	// StripControls, and labels still anchor the parse.
	sgrRe = regexp.MustCompile("\x1b\\[[0-9;]*m")
	// "└  0 credentials": the authoritative count of the Credentials
	// section. The singular "1 credential" reads too. Separators are
	// blanks, never newlines, so a wrapped line cannot form a false row.
	credentialsRe = regexp.MustCompile(`(?m)^[^│\n]*└[ \t]+(\d+)[ \t]+credentials?[ \t]*$`)
	// "┌  Environment": the env section header, when one is shown.
	envHeaderRe = regexp.MustCompile(`(?m)^[^│\n]*┌[ \t]+Environment[ \t]*$`)
	// "└  1 environment variable": its count, singular or plural.
	envCountRe = regexp.MustCompile(`(?m)^[^│\n]*└[ \t]+(\d+)[ \t]+environment variables?[ \t]*$`)
	// "●  DeepSeek DEEPSEEK_API_KEY": one provider row. The name is one
	// word; the detail is the rest of the row as shown.
	providerRowRe = regexp.MustCompile(`(?m)^[^●\n]*●[ \t]+(\S+)[ \t]+(\S(?:.*\S)?)[ \t]*$`)
)

// ParseProviders reads an `opencode providers list` output (01b C2) as of
// Version. A screen with no credentials count yields FAILED; a `●` row in
// an unknown shape yields PARTIAL with the counts kept, never dropped. It
// never guesses and never opens a credential file.
func ParseProviders(screen string) *Providers {
	p := &Providers{}
	if len(screen) > harness.MaxScreenBytes {
		p.ParseOutcome = typev1.ParseOutcome_PARSE_OUTCOME_FAILED
		return p
	}
	screen = harness.StripControls(sgrRe.ReplaceAllString(screen, ""))

	m := credentialsRe.FindStringSubmatch(screen)
	if m == nil {
		p.ParseOutcome = typev1.ParseOutcome_PARSE_OUTCOME_FAILED
		return p
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		p.ParseOutcome = typev1.ParseOutcome_PARSE_OUTCOME_FAILED
		return p
	}
	p.Credentials = n

	complete := true
	for _, row := range providerRowRe.FindAllStringSubmatch(screen, -1) {
		name, detail := row[1], strings.TrimSpace(row[2])
		if isEnvRow(screen, row[0]) {
			p.EnvProviders = append(p.EnvProviders, Provider{Name: name, Detail: detail})
		} else {
			p.CredentialProviders = append(p.CredentialProviders, Provider{Name: name, Detail: detail})
		}
	}
	// A bullet with no name or detail the row pattern cannot read is not
	// dropped silently: the reading turns partial.
	for line := range strings.Lines(screen) {
		if strings.Contains(line, "●") && providerRowRe.FindString(line) == "" {
			complete = false
		}
	}
	if envHeaderRe.FindString(screen) != "" {
		p.EnvSeen = true
		if m := envCountRe.FindStringSubmatch(screen); m != nil {
			if n, err := strconv.Atoi(m[1]); err == nil {
				p.EnvCount = n
			} else {
				complete = false
			}
		} else {
			complete = false
		}
	}

	if complete {
		p.ParseOutcome = typev1.ParseOutcome_PARSE_OUTCOME_OK
	} else {
		p.ParseOutcome = typev1.ParseOutcome_PARSE_OUTCOME_PARTIAL
	}
	return p
}

// isEnvRow tells whether a provider row sits under the Environment header:
// what follows the header up to the row holds no Credentials header.
func isEnvRow(screen, row string) bool {
	envAt := envHeaderRe.FindStringIndex(screen)
	rowAt := strings.Index(screen, row)
	if envAt == nil || rowAt < 0 || envAt[0] > rowAt {
		return false
	}
	return true
}

// ModelLine is one parsed model line under the TUI input box (01b C2): the
// agent before the first `·`, the model and provider as shown, and the
// variant after a second `·` when one is set ("high"). The model and the
// provider are not split apart: only the harness knows where its model
// name ends, so the probe matches Selection against the providers list
// instead of the parser guessing the cut.
type ModelLine struct {
	Agent        string
	Selection    string
	Variant      string
	ParseOutcome typev1.ParseOutcome
}

// modelLineRe is the `┃  Build · Big Pickle OpenCode Zen` row: the box edge
// keeps it from matching a stray `·` in other text. Blanks are spaces and
// tabs only: `\s` would reach across lines and glue the input box to the
// model line.
var modelLineRe = regexp.MustCompile(`(?m)^[^·\n]*┃[ \t]*([^·\n]+?)[ \t]*·[ \t]*([^\n]+?)[ \t]*$`)

// ParseModelLine reads the TUI model line (01b C2) as of Version. A screen
// with no model line yields FAILED. It never guesses.
func ParseModelLine(screen string) *ModelLine {
	m := &ModelLine{}
	if len(screen) > harness.MaxScreenBytes {
		m.ParseOutcome = typev1.ParseOutcome_PARSE_OUTCOME_FAILED
		return m
	}
	match := modelLineRe.FindStringSubmatch(harness.StripControls(sgrRe.ReplaceAllString(screen, "")))
	if match == nil {
		m.ParseOutcome = typev1.ParseOutcome_PARSE_OUTCOME_FAILED
		return m
	}
	m.Agent = strings.TrimSpace(match[1])
	rest := strings.TrimSpace(match[2])
	if head, tail, ok := strings.Cut(rest, "·"); ok {
		m.Selection, m.Variant = strings.TrimSpace(head), strings.TrimSpace(tail)
	} else {
		m.Selection = rest
	}
	if m.Agent == "" || m.Selection == "" {
		m.ParseOutcome = typev1.ParseOutcome_PARSE_OUTCOME_FAILED
		return m
	}
	m.ParseOutcome = typev1.ParseOutcome_PARSE_OUTCOME_OK
	return m
}
