package opencode

import (
	"slices"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// classes is the C4 command catalog (01b): the 01b class of each OpenCode
// slash command, as shown with its leading slash. The table's own examples
// keep their class: /status and /diff read state (observe); /review reads
// changes back (review); /exit ends the app (destructive). OpenCode has no
// /usage and no /goal (01b), so neither appears here. A name not on this
// list is DENY: the 01b never-send list (/login, /logout, /permissions,
// /config, /hooks, /plugin(s), /sandbox, usage-buying) holds on every
// harness, and a discovered command whose effect is unprobed (/models
// switches the model, /connect adds a provider, /init writes setup files)
// is never guessed into a sendable class. Local policy may move a command
// to a stricter class, never a looser one.
var classes = map[string]typev1.CommandClass{
	"/status": typev1.CommandClass_COMMAND_CLASS_OBSERVE,
	"/diff":   typev1.CommandClass_COMMAND_CLASS_OBSERVE,

	"/review": typev1.CommandClass_COMMAND_CLASS_REVIEW,

	"/exit": typev1.CommandClass_COMMAND_CLASS_DESTRUCTIVE,
}

// Classify returns the 01b class of one OpenCode slash command, as shown
// with its leading slash (C4). The match is exact: a missing slash,
// different case, or trailing text is not the shown name and is DENY.
// Unknown commands are DENY.
func Classify(command string) typev1.CommandClass {
	if class, ok := classes[command]; ok {
		return class
	}
	return typev1.CommandClass_COMMAND_CLASS_DENY
}

// CommandNames lists every slash command the catalog classifies, sorted,
// as shown with the leading slash. The agent hashes this list for the F8
// catalog check (C4); a name added here changes the hash.
func CommandNames() []string {
	names := make([]string, 0, len(classes))
	for name := range classes {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}
