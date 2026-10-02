#!/usr/bin/env bash
# PreToolUse hook for file edits (Claude: Edit|MultiEdit|Write; Muse:
# write_file, edit_file). Refuses an edit to a surface governed by a project
# skill until that skill has been loaded in this context window.
#
# The path-to-skill map is data: .agents/skill-paths.json. skill-loaded.sh
# records every skill actually loaded as a marker file, so this checks loading,
# not acknowledgement: retrying without loading is refused again. A refusal is
# exit 2 with the reason on stderr, which both Claude Code and Muse feed back
# to the model.
#
# Payloads: Claude sends tool_input.file_path (absolute); Muse sends
# tool_input.path, possibly relative to the payload cwd. Both are read here.
#
# Fail-open on anything unexpected (no python3, unreadable map, path outside a
# fleet-board tree): a hook that can refuse an edit must never be the reason a
# session breaks. Escape hatches: see docs/development/hooks.md.
set -u

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=.claude/hooks/lib/common.sh
. "$here/lib/common.sh"

payload="$(cat)"
command -v python3 >/dev/null 2>&1 || exit 0

{
	IFS= read -r sid
	IFS= read -r fp
	IFS= read -r cwd
} < <(printf '%s' "$payload" | python3 "$here/lib/payload.py" \
	session_id 'tool_input.file_path|tool_input.path' cwd)

[ -n "$fp" ] || exit 0
case "$fp" in
	/*) ;;
	*) [ -n "$cwd" ] && fp="${cwd%/}/$fp" ;;
esac

root="$(fleet_root_path "$fp")" || exit 0
fleet_hooks_off "$root" && exit 0
rel="${fp#"$root"/}"

{
	IFS= read -r ids
	IFS= read -r notes
} < <(python3 "$here/lib/skill_paths.py" match "$root" "$rel" 2>/dev/null)
[ -n "${ids:-}" ] || exit 0

sid12="$(fleet_sid12 "$sid")"
state="$(fleet_state_dir)"
missing=""
for id in $ids; do
	[ -f "$state/fleet-skill-loaded-${sid12}-${id}" ] && continue
	missing="${missing}${missing:+ }${id}"
done
[ -n "$missing" ] || exit 0

# Deliberate, per-session escape hatch for a skill that genuinely cannot be
# loaded. Nothing relaxes on its own: a silent fallback would hide exactly the
# case worth knowing about.
override="$state/fleet-skill-override-${sid12}"
[ -f "$override" ] && exit 0

{
	printf 'BLOCKED (skill-nudge): %s\n' "$rel"
	printf 'This path is governed by project skill(s) not loaded in this context: %s\n' "$missing"
	[ -n "${notes:-}" ] && printf 'Note: %s\n' "$notes"
	printf 'Load each with the skill tool, then retry. Compaction clears loaded skills, so having loaded one earlier does not count.\n'
	printf 'If a skill genuinely cannot be loaded here, say so and run: touch %s\n' "$override"
} >&2
exit 2
