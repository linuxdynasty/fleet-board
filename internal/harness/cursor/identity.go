package cursor

import (
	"regexp"
	"strings"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// Identity is one parsed account-identity output (01b C2, 0.22): the
// signed-in email as the harness shows it. The agent reads identity from
// these commands only, never from ~/.cursor/cli-config.json, which also
// holds auth material.
type Identity struct {
	Email        string
	ParseOutcome typev1.ParseOutcome
}

var (
	// "✓ Logged in as <email>": `cursor-agent status` names the account on
	// its own line. The anchor is the line start (the check mark is
	// optional): a line that merely mentions logging in must never read
	// as identity.
	statusEmailRe = regexp.MustCompile(`(?m)^(?:✓\s+)?Logged in as\s+(\S+)\s*$`)
	// "User Email          <email>": `cursor-agent about` names it on
	// its own row. Other rows (plan, CLI version, a newer "Latest"
	// line) never decide identity.
	aboutEmailRe = regexp.MustCompile(`(?m)^User Email\s+(\S+)\s*$`)
)

// ParseIdentity reads a `cursor-agent status` or `about` output (01b C2)
// as of Version. status wins when both markers show; an output with
// neither, or with a marker naming no address, fails with no reading. It
// never guesses and never opens a credential file.
func ParseIdentity(output string) *Identity {
	id := &Identity{}
	if len(output) > maxScreenBytes {
		id.ParseOutcome = typev1.ParseOutcome_PARSE_OUTCOME_FAILED
		return id
	}
	out := stripControls(output)
	email := ""
	if m := statusEmailRe.FindStringSubmatch(out); m != nil {
		email = m[1]
	} else if m := aboutEmailRe.FindStringSubmatch(out); m != nil {
		email = m[1]
	}
	if !strings.Contains(email, "@") {
		id.ParseOutcome = typev1.ParseOutcome_PARSE_OUTCOME_FAILED
		return id
	}
	id.Email = email
	id.ParseOutcome = typev1.ParseOutcome_PARSE_OUTCOME_OK
	return id
}
