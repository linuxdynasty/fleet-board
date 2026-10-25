#!/usr/bin/env bash
# Stop hook (Claude Code Stop; Muse Stop). If this session has a declared,
# unfinished goal, do not let the turn end on a status report: hand the goal
# back and make the agent continue. Prose in AGENTS.md did not stop idling; a
# Stop hook runs whether or not the model remembered the rule.
#
# The decision lives in lib/goal_stop.py (goal file protocol, DONE / BLOCKED
# WATCH= / BLOCKED OWNER= ACTION=, per-session no-progress budget). This wrapper
# only applies the off switches and fails open: exit 0 always, so a broken
# environment never traps a session.
#
# Off switches: CLAUDE_GOAL_OFF=1, FLEET_HOOKS_OFF=1, or an empty
# .claude/hooks-off file. See docs/development/hooks.md.
set -u

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=.claude/hooks/lib/common.sh
. "$here/lib/common.sh"

command -v python3 >/dev/null 2>&1 || exit 0
payload="$(cat)"
[ -n "$payload" ] || exit 0
[ "${CLAUDE_GOAL_OFF:-0}" = "1" ] && exit 0

cwd="$(printf '%s' "$payload" | python3 "$here/lib/payload.py" cwd)"
root="$(fleet_root_path "${cwd:-$PWD}")" || root=""
fleet_hooks_off "$root" && exit 0

printf '%s' "$payload" | python3 "$here/lib/goal_stop.py" || true
exit 0
