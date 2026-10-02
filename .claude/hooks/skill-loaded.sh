#!/usr/bin/env bash
# PostToolUse hook for skill loads (Claude: Skill; Muse: read_skill). Records
# which skills were loaded in this session so skill-nudge.sh can require them
# rather than merely suggest them, and on-compact.sh can list them again after
# compaction. Never blocks; its only job is to write a marker:
#   <state dir>/fleet-skill-loaded-<session id prefix>-<skill>
#
# The skill name is tool_input.skill (Claude) or tool_input.name (Muse). A
# plugin prefix is dropped (`pkg:fleet-go` records `fleet-go`).
set -u

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=.claude/hooks/lib/common.sh
. "$here/lib/common.sh"

payload="$(cat)"
command -v python3 >/dev/null 2>&1 || exit 0

{
	IFS= read -r sid
	IFS= read -r name
	IFS= read -r cwd
} < <(printf '%s' "$payload" | python3 "$here/lib/payload.py" \
	session_id 'tool_input.skill|tool_input.skill_name|tool_input.name' cwd)

[ -n "$name" ] || exit 0
root="$(fleet_root_path "${cwd:-$PWD}")" || root=""
fleet_hooks_off "$root" && exit 0

name="${name##*:}"
safe="$(printf '%s' "$name" | tr -cd 'a-zA-Z0-9-' | cut -c1-60)"
[ -n "$safe" ] || exit 0
touch "$(fleet_state_dir)/fleet-skill-loaded-$(fleet_sid12 "$sid")-${safe}" 2>/dev/null
exit 0
