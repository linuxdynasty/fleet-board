#!/usr/bin/env bash
# SessionStart hook (Claude Code matcher `compact|resume`; Muse SessionStart
# and PreCompact). Fires when context was compacted or resumed, the moment a
# long goal session loses its rules and its loaded skills.
#
# It does three things:
#   1. clears this session's loaded-skill markers (a resume keeps the session
#      id but discards skill CONTENT, so a marker would outlive what it stands
#      for and skill-nudge.sh would wave through an ungoverned edit);
#   2. re-injects a pointer to the AGENTS.md rules, the active goal, and the
#      names of the skills that had been loaded, so the agent can load them
#      again before its next edit;
#   3. stays silent on a fresh startup and outside a fleet-board checkout.
# It names the rules rather than restating them: rules live once, in AGENTS.md.
set -u

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=.claude/hooks/lib/common.sh
. "$here/lib/common.sh"

command -v python3 >/dev/null 2>&1 || exit 0
payload="$(cat)"
[ -n "$payload" ] || exit 0

{
	IFS= read -r sid
	IFS= read -r cwd
	IFS= read -r event
	IFS= read -r source_kind
} < <(printf '%s' "$payload" | python3 "$here/lib/payload.py" session_id cwd hook_event_name source)

# A brand-new session has nothing to restore (Claude's matcher already keeps
# this off startup; Muse registers it without a matcher).
[ "$event" = "SessionStart" ] && [ "$source_kind" = "startup" ] && exit 0
[ -n "$sid" ] || exit 0

root="$(fleet_root_path "${cwd:-$PWD}")" || root=""
fleet_hooks_off "$root" && exit 0

# Record, then clear, this session's markers. Only this session's, so a
# concurrent session keeps its own. Done before the scope check: a stale marker
# is wrong whatever the checkout.
state="$(fleet_state_dir)"
sid12="$(fleet_sid12 "$sid")"
loaded=""
for marker in "$state"/fleet-skill-loaded-"${sid12}"-*; do
	[ -e "$marker" ] || continue
	loaded="${loaded}${loaded:+ }${marker##*"fleet-skill-loaded-${sid12}-"}"
	rm -f "$marker" 2>/dev/null
done
[ -n "$root" ] || exit 0

{
	IFS= read -r goal_file
	IFS= read -r goal_consent
	goal_text="$(cat)"
} < <(python3 "$here/lib/goal_lib.py" describe "$sid" "$cwd" 2>/dev/null)

text="CONTEXT WAS COMPACTED OR RESUMED. Loaded skills are GONE from this context even if the summary says they were used: the skill listing survives, the skill content does not. Before your next tool call: (1) re-read ${root}/AGENTS.md, which holds the rules and the skill routing table; (2) confirm pwd is your worktree under ~/personal-repos/fleet-board-worktrees/, not the main checkout; (3) separate what the summary says was verified from what you can still cite a run for, and re-run a gate rather than trust a summarized pass."
if [ -n "$loaded" ]; then
	text="${text}

Skills loaded before this point, now gone, to load again before editing the surfaces they govern: ${loaded}."
fi
if [ -n "${goal_file:-}" ]; then
	text="${text}

ACTIVE GOAL (${goal_file}):
${goal_text}"
	[ -n "${goal_consent:-}" ] && text="${text}
OWNER CONSENT ALREADY GRANTED for: ${goal_consent}. Do not stop to ask again for those."
fi

fleet_emit_context SessionStart "$text"
exit 0
