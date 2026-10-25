package control

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/harness/claude"
	"github.com/linuxdynasty/fleet-board/internal/harness/codex"
	"github.com/linuxdynasty/fleet-board/internal/harness/cursor"
	"github.com/linuxdynasty/fleet-board/internal/harness/muse"
	"github.com/linuxdynasty/fleet-board/internal/harness/opencode"
)

// This file is the F8 allow-list (01b, C4): which slash commands each
// harness may be sent, and the catalog hash the hub sends back. The lists
// come from the adapters' own classification tables, so a command the
// adapter never reviewed is never in the catalog. Local policy moves a
// command to a stricter class only; there is no per-command policy yet, so
// nothing is tightened here.

// catalogNames lists the catalogued slash commands of one harness, sorted,
// as shown with the leading slash. It is nil for a harness with no table.
func catalogNames(harness string) []string {
	switch harness {
	case "claude":
		return claude.CommandNames()
	case "codex":
		return codex.CommandNames()
	case "muse":
		return muse.CommandNames()
	case "cursor":
		return cursor.CommandNames()
	case "opencode":
		return opencode.CommandNames()
	default:
		return nil
	}
}

// inCatalog reports whether the adapter catalogues the command for the
// harness. The match is exact, like Classify: a missing slash, different
// case or trailing text is another name.
func inCatalog(harness, command string) bool {
	names := catalogNames(harness)
	i := sort.SearchStrings(names, command)
	return i < len(names) && names[i] == command
}

// CatalogHash is the C4 catalog hash for one harness: the hex SHA-256 over
// the NUL-joined sorted entry names (harness.proto: "hex digest over the
// sorted entry names"). The hub sends it back with RunHarnessCommand, and
// the handler refuses a hash that is not this one, so a command typed
// against a changed catalog fails closed. It is empty for a harness with
// no table, which never matches.
func CatalogHash(harness string) string {
	names := catalogNames(harness)
	if len(names) == 0 {
		return ""
	}
	sum := sha256.Sum256([]byte(strings.Join(names, "\x00")))
	return hex.EncodeToString(sum[:])
}

// catalogClass returns the adapter's 01b class for one catalogued command.
// An uncatalogued name is DENY, the same answer Classify gives it.
func catalogClass(harness, command string) typev1.CommandClass {
	if !inCatalog(harness, command) {
		return typev1.CommandClass_COMMAND_CLASS_DENY
	}
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
