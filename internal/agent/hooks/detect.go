package hooks

import (
	"os"
	"path/filepath"
	"strings"
)

// projectConfigs are the repo-level hook files read by step 1 of
// EnsureGoalHooks. A project Stop hook or a goal router on
// UserPromptSubmit means the repo owns its goals (eshu): the fleet hooks
// treat that workdir as inert and nothing in the repo is changed.
var projectConfigs = []string{
	".claude/settings.json",
	".claude/settings.local.json",
	".codex/hooks.json",
	".muse/hooks.json",
	".cursor/hooks.json",
}

// RepoShipsOwn reports whether the checkout at workdir ships its own goal
// hooks. The detail names the deciding file and event (paths only, never
// file contents). An unparseable project file reads as repo-owned: the
// agent never fights repo hooks it cannot see.
func RepoShipsOwn(workdir string) (bool, string) {
	for _, rel := range projectConfigs {
		raw, err := os.ReadFile(filepath.Join(workdir, rel)) //nolint:gosec // G304: the path joins the checkout with a fixed project file name
		if err != nil {
			continue
		}
		obj, perr := parseObject(raw)
		if perr != nil {
			return true, rel + ": unparseable project hooks"
		}
		hi := obj.index("hooks")
		if hi < 0 {
			continue
		}
		section, serr := parseObject(obj.val[hi])
		if serr != nil {
			return true, rel + ": unparseable project hooks"
		}
		for i, event := range section.keys {
			arr, aerr := parseArray(section.val[i])
			if aerr != nil || len(arr.elems) == 0 {
				continue
			}
			if strings.EqualFold(event, "stop") {
				return true, rel + ": project Stop hook"
			}
			if strings.EqualFold(event, "userpromptsubmit") ||
				strings.EqualFold(event, "beforesubmitprompt") {
				if strings.Contains(strings.ToLower(string(section.val[i])), "goal") {
					return true, rel + ": goal router on " + event
				}
			}
		}
	}
	return false, ""
}
