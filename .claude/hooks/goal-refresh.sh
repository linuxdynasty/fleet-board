#!/usr/bin/env bash
# UserPromptSubmit hook (Claude Code; Muse). The producer and refresher for the
# goal file the Stop hook reads: `/goal <text>` writes it, and every prompt
# restates the active goal so it survives long sessions and compaction. Without
# a producer the Stop hook would be a consumer with nothing to consume.
#
# Logic is in lib/goal_refresh.py. Exit 0 always: this runs on every prompt the
# owner types and must never be the reason one fails to send.
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

printf '%s' "$payload" | python3 "$here/lib/goal_refresh.py" || true
exit 0
