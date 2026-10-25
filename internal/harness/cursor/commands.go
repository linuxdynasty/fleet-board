package cursor

import (
	"slices"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// sendable classes the recorded probes support: the command, then its 01b
// class. /usage shows the plan (observe), /model picks the model (session),
// /goal <objective> starts the durable goal (goal, 01b2), and /quit ends
// the session (destructive). A name not on this list is DENY: the 01b
// never-send list (/login, /logout, /permissions, /config, /hooks,
// /plugin(s), /sandbox, usage-buying) holds on every harness, and a
// discovered command whose effect is unprobed is never guessed into a
// sendable class.
var sendable = map[string]typev1.CommandClass{
	"/usage": typev1.CommandClass_COMMAND_CLASS_OBSERVE,
	"/model": typev1.CommandClass_COMMAND_CLASS_SESSION,
	"/goal":  typev1.CommandClass_COMMAND_CLASS_GOAL,
	"/quit":  typev1.CommandClass_COMMAND_CLASS_DESTRUCTIVE,
}

// Classify returns the 01b class of one `cursor-agent` slash command, as
// shown with its leading slash (C4). The match is exact: a missing slash,
// different case, or trailing text is not the shown name and is DENY.
// Unknown commands are DENY.
func Classify(command string) typev1.CommandClass {
	if class, ok := sendable[command]; ok {
		return class
	}
	return typev1.CommandClass_COMMAND_CLASS_DENY
}

// CommandNames lists every slash command the catalog classifies, sorted,
// as shown with the leading slash. The agent hashes this list for the F8
// catalog check (C4); a name added here changes the hash.
func CommandNames() []string {
	names := make([]string, 0, len(sendable))
	for name := range sendable {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}
