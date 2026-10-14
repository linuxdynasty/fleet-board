package claude

import (
	"strings"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/harness"
)

// ReadyParserVersion is this screen parser's own version: bump it when its
// behavior changes (01b, fleet-harness-adapter). Usage parsing has its own
// ParserVersion in usage.go.
const ReadyParserVersion = "1"

// Ready is one parsed Claude Code screen for the READY feature (01 C3):
// whether the folder-trust box shows instead of a prompt. The box is the
// FOLDER_UNTRUSTED signal: the owner must trust the folder by hand, and the
// probe types nothing. Prompt detection is a later slice, so any screen
// without the box fails cleanly instead of guessing ready.
type Ready struct {
	TrustBox     bool
	ParseOutcome typev1.ParseOutcome
}

// ParseReady reads a Claude Code screen for the folder-trust box: the
// "Accessing workspace:" heading together with its "Yes, I trust this
// folder" answer row. Both must show; a heading without the row (a
// truncated capture) is not trust. It never guesses.
func ParseReady(screen string) *Ready {
	r := &Ready{ParseOutcome: typev1.ParseOutcome_PARSE_OUTCOME_FAILED}
	if len(screen) > harness.MaxScreenBytes {
		return r
	}
	if strings.Contains(screen, "Accessing workspace:") &&
		strings.Contains(screen, "Yes, I trust this folder") {
		r.TrustBox = true
		r.ParseOutcome = typev1.ParseOutcome_PARSE_OUTCOME_OK
	}
	return r
}
