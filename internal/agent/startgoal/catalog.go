package startgoal

import (
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/harness/claude"
	"github.com/linuxdynasty/fleet-board/internal/harness/codex"
	"github.com/linuxdynasty/fleet-board/internal/harness/cursor"
	"github.com/linuxdynasty/fleet-board/internal/harness/muse"
	"github.com/linuxdynasty/fleet-board/internal/harness/opencode"
)

// commandClass reads one slash command's 01b class from the harness's
// own catalog (C4): the agent never guesses a class. Unknown commands
// and unknown harnesses are DENY.
func commandClass(harness, command string) typev1.CommandClass {
	switch harness {
	case "claude":
		return claude.Classify(command)
	case "codex":
		return codex.Classify(command)
	case "muse":
		return muse.Classify(command)
	case "cursor":
		return cursor.Classify(command)
	case "opencode":
		return opencode.Classify(command)
	default:
		return typev1.CommandClass_COMMAND_CLASS_DENY
	}
}
