#!/usr/bin/env bash
# Sourced by the hooks in .claude/hooks/ -- not run on its own.
#
# Shared by Claude Code, Codex and Muse wiring, so everything here must work
# with a cleared environment (Muse hooks see only HOME, LANG, PATH and a few
# more) and on the macOS system bash 3.2: no arrays, no `declare -A`, no
# `mapfile`.

# Where per-session markers and de-dup state live. FLEET_HOOK_STATE_DIR lets
# tests use a scratch directory.
fleet_state_dir() {
	local d="${FLEET_HOOK_STATE_DIR:-${TMPDIR:-/tmp}}"
	printf '%s\n' "${d%/}"
}

# A session id reduced to a filename-safe prefix. Empty becomes "na".
fleet_sid12() { # session_id
	local s
	s="$(printf '%s' "${1:-}" | tr -c 'A-Za-z0-9._-' '-' | cut -c1-12)"
	printf '%s\n' "${s:-na}"
}

# Prints the checkout root (the directory holding .agents/skill-paths.json)
# that contains a file or directory. Works from the main checkout and from any
# worktree; fails for a path outside a fleet-board tree, which is how these
# hooks stay inert elsewhere.
fleet_root_path() { # path
	local d="${1:-}"
	[ -n "$d" ] || return 1
	[ -d "$d" ] || d="$(dirname "$d")"
	while [ -n "$d" ] && [ "$d" != "/" ] && [ "$d" != "." ]; do
		if [ -f "$d/.agents/skill-paths.json" ]; then
			printf '%s\n' "$d"
			return 0
		fi
		d="$(dirname "$d")"
	done
	return 1
}

# True when the developer switched every dev hook off locally: FLEET_HOOKS_OFF=1
# in the environment, or an empty file .claude/hooks-off in the checkout (the
# file form is the one that reaches Muse, whose hooks run with a cleared
# environment). The file is gitignored.
fleet_hooks_off() { # root
	[ "${FLEET_HOOKS_OFF:-0}" = "1" ] && return 0
	[ -n "${1:-}" ] && [ -e "$1/.claude/hooks-off" ] && return 0
	return 1
}

# Prints a hook JSON result carrying additionalContext.
fleet_emit_context() { # event text
	FLEET_EVENT="$1" FLEET_TEXT="$2" python3 -c '
import json, os
print(json.dumps({"hookSpecificOutput": {
    "hookEventName": os.environ["FLEET_EVENT"],
    "additionalContext": os.environ["FLEET_TEXT"]}}))
' 2>/dev/null
}
