package codex

import (
	"strings"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/harness"
)

// ReadyParserVersion is this screen parser's own version: bump it when its
// behavior changes (01b, fleet-harness-adapter). Usage parsing has its own
// ParserVersion in usage.go.
const ReadyParserVersion = "1"

// Ready is one parsed Codex screen for the READY feature (01 C3): whether
// the folder-access box shows instead of a prompt. The box is the
// FOLDER_UNTRUSTED signal: the owner must trust the folder by hand, and the
// probe types nothing. Prompt detection is a later slice, so any screen
// without the box fails cleanly instead of guessing ready.
type Ready struct {
	TrustBox     bool
	ParseOutcome typev1.ParseOutcome
}

// ParseReady reads a Codex screen for the folder-access box: the "Folder
// access" heading together with its "1. Trust and continue" answer row.
// Both must show; a heading without the row (a truncated capture) is not
// trust. The update and daemon-settings startup screens carry neither, so
// they fail cleanly. It never guesses.
func ParseReady(screen string) *Ready {
	r := &Ready{ParseOutcome: typev1.ParseOutcome_PARSE_OUTCOME_FAILED}
	if len(screen) > harness.MaxScreenBytes {
		return r
	}
	if strings.Contains(screen, "Folder access") &&
		strings.Contains(screen, "1. Trust and continue") {
		r.TrustBox = true
		r.ParseOutcome = typev1.ParseOutcome_PARSE_OUTCOME_OK
	}
	return r
}
