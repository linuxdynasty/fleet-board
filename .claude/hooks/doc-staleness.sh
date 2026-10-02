#!/usr/bin/env bash
# PostToolUse hook after file edits (Claude: Edit|MultiEdit|Write; Codex:
# apply_patch; Muse: write_file, edit_file). Reminds the agent of documentation
# an edit has left behind: Go doc freshness (doc.go, README.md and AGENTS.md
# per package) and, when a .proto changed, the contract specs that must change
# in the same PR. The checks are in scripts/check-docs-stale.py, which reads
# `git status` and ignores the payload, because Codex's apply_patch payload
# names several files in a version-dependent shape.
#
# Never blocks. The reminder reaches the model as additionalContext, and the
# same text is not repeated until it changes (state in the hook state dir).
# Whether Codex and Muse surface PostToolUse additionalContext is not probed;
# a harness that ignores it loses only the reminder.
set -u

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=.claude/hooks/lib/common.sh
. "$here/lib/common.sh"

command -v python3 >/dev/null 2>&1 || exit 0
payload="$(cat)"

{
	IFS= read -r sid
	IFS= read -r cwd
} < <(printf '%s' "$payload" | python3 "$here/lib/payload.py" session_id cwd)

dir="${cwd:-$PWD}"
root="$(fleet_root_path "$dir")" || exit 0
fleet_hooks_off "$root" && exit 0

report="$(python3 "$here/../../scripts/check-docs-stale.py" --root "$root" 2>/dev/null)"
[ -n "$report" ] || exit 0

seen="$(fleet_state_dir)/fleet-doc-stale-$(fleet_sid12 "$sid")"
sum="$(printf '%s' "$report" | cksum | cut -d' ' -f1)"
[ "$(cat "$seen" 2>/dev/null)" = "$sum" ] && exit 0
printf '%s' "$sum" >"$seen" 2>/dev/null

fleet_emit_context PostToolUse "$(printf '%s\n' "$report" | sed 's/^/doc-staleness: /')"
exit 0
