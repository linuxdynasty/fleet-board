package codex

import (
	"slices"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// classes is the C4 command catalog (01b): the 01b class of each Codex
// slash command, as shown with its leading slash. The table's own examples
// keep their class: /status and /usage read state (observe); /compact,
// /model, /fast, /plan, /rename, /resume, /stop and /fork shape the session
// (session); /goal is the durable goal (goal, 01b2); /review reads changes
// back (review); /clear, /rewind, /delete, /archive, /exit and /quit end or
// drop the session (destructive). A name not on this list is DENY: the 01b
// never-send list (/login, /logout, /permissions, /config, /hooks,
// /plugin(s), /sandbox, usage-buying) holds on every harness, and a
// discovered command whose effect is unprobed is never guessed into a
// sendable class. Local policy may move a command to a stricter class, never
// a looser one.
var classes = map[string]typev1.CommandClass{
	"/status":  typev1.CommandClass_COMMAND_CLASS_OBSERVE,
	"/usage":   typev1.CommandClass_COMMAND_CLASS_OBSERVE,
	"/context": typev1.CommandClass_COMMAND_CLASS_OBSERVE,
	"/diff":    typev1.CommandClass_COMMAND_CLASS_OBSERVE,
	"/ps":      typev1.CommandClass_COMMAND_CLASS_OBSERVE,
	"/tasks":   typev1.CommandClass_COMMAND_CLASS_OBSERVE,
	"/recap":   typev1.CommandClass_COMMAND_CLASS_OBSERVE,
	"/hud":     typev1.CommandClass_COMMAND_CLASS_OBSERVE,

	"/compact": typev1.CommandClass_COMMAND_CLASS_SESSION,
	"/model":   typev1.CommandClass_COMMAND_CLASS_SESSION,
	"/effort":  typev1.CommandClass_COMMAND_CLASS_SESSION,
	"/fast":    typev1.CommandClass_COMMAND_CLASS_SESSION,
	"/plan":    typev1.CommandClass_COMMAND_CLASS_SESSION,
	"/rename":  typev1.CommandClass_COMMAND_CLASS_SESSION,
	"/resume":  typev1.CommandClass_COMMAND_CLASS_SESSION,
	"/stop":    typev1.CommandClass_COMMAND_CLASS_SESSION,
	"/fork":    typev1.CommandClass_COMMAND_CLASS_SESSION,

	"/goal": typev1.CommandClass_COMMAND_CLASS_GOAL,

	"/review":          typev1.CommandClass_COMMAND_CLASS_REVIEW,
	"/code-review":     typev1.CommandClass_COMMAND_CLASS_REVIEW,
	"/security-review": typev1.CommandClass_COMMAND_CLASS_REVIEW,

	"/clear":   typev1.CommandClass_COMMAND_CLASS_DESTRUCTIVE,
	"/rewind":  typev1.CommandClass_COMMAND_CLASS_DESTRUCTIVE,
	"/delete":  typev1.CommandClass_COMMAND_CLASS_DESTRUCTIVE,
	"/archive": typev1.CommandClass_COMMAND_CLASS_DESTRUCTIVE,
	"/exit":    typev1.CommandClass_COMMAND_CLASS_DESTRUCTIVE,
	"/quit":    typev1.CommandClass_COMMAND_CLASS_DESTRUCTIVE,
}

// Classify returns the 01b class of one `codex` slash command, as shown
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
