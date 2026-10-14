package muse

import (
	"strings"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/harness"
)

// ReadyParserVersion is this screen parser's own version: bump it when its
// behavior changes (01b, fleet-harness-adapter). Usage parsing has its own
// ParserVersion in usage.go.
const ReadyParserVersion = "1"

// Ready is one parsed Muse screen for the READY feature (01 C3): whether
// the workspace-trust box shows instead of a prompt. The box is the
// FOLDER_UNTRUSTED signal: the owner must trust the folder by hand, and the
// probe types nothing. Prompt detection is a later slice, so any screen
// without the box fails cleanly instead of guessing ready.
type Ready struct {
	TrustBox     bool
	ParseOutcome typev1.ParseOutcome
}

// ParseReady reads a Muse screen for the workspace-trust box: the "Do
// you trust this workspace?" question together with its "Trust and
// continue" answer row. Both must show; a question without the row (a
// truncated capture) is not trust. It never guesses, and it does not depend
// on the placeholder workspace names in the fixtures.
func ParseReady(screen string) *Ready {
	r := &Ready{ParseOutcome: typev1.ParseOutcome_PARSE_OUTCOME_FAILED}
	if len(screen) > harness.MaxScreenBytes {
		return r
	}
	if strings.Contains(screen, "Do you trust this workspace?") &&
		strings.Contains(screen, "Trust and continue") {
		r.TrustBox = true
		r.ParseOutcome = typev1.ParseOutcome_PARSE_OUTCOME_OK
	}
	return r
}
