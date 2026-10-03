#!/usr/bin/env bash
#
# test-dev-hooks.sh: self-test for the development hooks (docs/development/hooks.md).
#
# Feeds synthetic hook payloads (JSON on stdin) to the real scripts and checks
# exit codes and output. Also runs the Python suites for the goal role router and
# the Codex spawn guard, and drives the three harness configs the way each
# harness would (Muse with a cleared environment). Runs on the macOS system
# bash 3.2: no arrays, no associative arrays, no mapfile.
#
# Usage: scripts/test-dev-hooks.sh        exit 0 only if every case passes
set -u

root="$(cd "$(dirname "$0")/.." && pwd)"
hooks="$root/.claude/hooks"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

# Isolate every hook from the developer's real state and goal files.
export FLEET_HOOK_STATE_DIR="$work/state"
export HOME="$work/home"
mkdir -p "$FLEET_HOOK_STATE_DIR" "$HOME"
unset CLAUDE_GOAL_OFF CLAUDE_GOAL_FILE CLAUDE_GOAL_CONSENT FLEET_HOOKS_OFF CLAUDE_PROJECT_DIR CODEX_PROJECT_DIR

pass=0
fail=0
out=""
err=""
rc=0

ok() { pass=$((pass + 1)); }
bad() {
	fail=$((fail + 1))
	printf 'FAIL: %s\n' "$1" >&2
	[ -n "$out" ] && printf '  stdout: %s\n' "$out" >&2
	[ -n "$err" ] && printf '  stderr: %s\n' "$err" >&2
}

# run <script> <payload> [env assignments...]: sets out, err, rc.
run() {
	local script="$1" payload="$2"
	shift 2
	out="$(printf '%s' "$payload" | env "$@" "$script" 2>"$work/stderr")"
	rc=$?
	err="$(cat "$work/stderr")"
}

expect_rc() { # name expected
	if [ "$rc" -eq "$2" ]; then ok; else bad "$1: exit $rc, want $2"; fi
}
expect_contains() { # name haystack needle
	case "$2" in *"$3"*) ok ;; *) bad "$1: missing '$3'" ;; esac
}
expect_empty() { # name value
	if [ -z "$2" ]; then ok; else bad "$1: expected no output"; fi
}
json_field() { # json python-expr over d
	printf '%s' "$1" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(eval(sys.argv[1]))' "$2" 2>/dev/null
}

sid() { printf 'sess-%s' "$1"; }
skill_payload() { # session skill
	printf '{"hook_event_name":"PostToolUse","tool_name":"Skill","session_id":"%s","cwd":"%s","tool_input":{"skill":"%s"}}' "$1" "$root" "$2"
}
edit_payload() { # session path
	printf '{"hook_event_name":"PreToolUse","tool_name":"Write","session_id":"%s","cwd":"%s","tool_input":{"file_path":"%s"}}' "$1" "$root" "$2"
}

# ---------------------------------------------------------------- skill-nudge
s="$(sid nudge)"
run "$hooks/skill-nudge.sh" "$(edit_payload "$s" "$root/api/fleet/x.proto")"
expect_rc "proto edit refused without the skill" 2
expect_contains "refusal names the skill" "$err" "fleet-contract-rigor"

run "$hooks/skill-loaded.sh" "$(skill_payload "$s" fleet-contract-rigor)"
expect_rc "skill-loaded exits 0" 0
run "$hooks/skill-nudge.sh" "$(edit_payload "$s" "$root/api/fleet/x.proto")"
expect_rc "proto edit allowed once the skill is recorded" 0

run "$hooks/skill-nudge.sh" "$(edit_payload "$s" "$root/internal/hub/store/db.go")"
expect_rc "store go file needs more skills" 2
expect_contains "union: fleet-go named" "$err" "fleet-go"
expect_contains "union: fleet-sqlite-store named" "$err" "fleet-sqlite-store"
run "$hooks/skill-loaded.sh" "$(skill_payload "$s" "pkg:fleet-go")"
run "$hooks/skill-nudge.sh" "$(edit_payload "$s" "$root/internal/hub/store/db.go")"
expect_contains "plugin prefix dropped, only the store skill still missing" "$err" "fleet-sqlite-store"
case "$err" in *"not loaded in this context: fleet-go"*) bad "fleet-go should count as loaded" ;; *) ok ;; esac

run "$hooks/skill-nudge.sh" "$(printf '{"session_id":"%s","cwd":"%s","tool_input":{"path":"web/src/App.tsx"}}' "$s" "$root")"
expect_rc "Muse-shaped payload (relative tool_input.path) is governed" 2
expect_contains "web path maps to fleet-frontend" "$err" "fleet-frontend"

s2="$(sid planpaths)"
run "$hooks/skill-nudge.sh" "$(edit_payload "$s2" "$root/internal/hub/ca/ca.go")"
expect_contains "hub CA code needs fleet-security" "$err" "fleet-security"
run "$hooks/skill-nudge.sh" "$(edit_payload "$s2" "$root/internal/hub/ingest/writer.go")"
expect_contains "ingest writer needs fleet-sqlite-store" "$err" "fleet-sqlite-store"
run "$hooks/skill-nudge.sh" "$(edit_payload "$s2" "$root/internal/agent/probe/session.go")"
expect_contains "probe sessions need fleet-tmux" "$err" "fleet-tmux"
run "$hooks/skill-nudge.sh" "$(edit_payload "$s2" "$root/internal/hub/ingest/README.md")"
expect_rc "docs inside those packages are not governed" 0

run "$hooks/skill-nudge.sh" "$(edit_payload "$s" "$root/docs/design.md")"
expect_rc "unmapped doc path is allowed" 0
run "$hooks/skill-nudge.sh" "$(edit_payload "$s" "$root/docs/specs/04-sqlite-notes.md")"
expect_rc "docs excluded from the sqlite rule" 0
run "$hooks/skill-nudge.sh" "$(edit_payload "$s" "/tmp/elsewhere/api/x.proto")"
expect_rc "path outside a fleet-board tree is ignored" 0
run "$hooks/skill-nudge.sh" "not json"
expect_rc "malformed payload fails open" 0

run "$hooks/skill-nudge.sh" "$(edit_payload "$s" "$root/internal/harness/claude/parse.go")" FLEET_HOOKS_OFF=1
expect_rc "FLEET_HOOKS_OFF=1 disables the nudge" 0
touch "$FLEET_HOOK_STATE_DIR/fleet-skill-override-$(printf '%s' "$s" | cut -c1-12)"
run "$hooks/skill-nudge.sh" "$(edit_payload "$s" "$root/internal/harness/claude/parse.go")"
expect_rc "per-session override file lifts the nudge" 0
rm -f "$FLEET_HOOK_STATE_DIR"/fleet-skill-override-*

# ------------------------------------------------------------------ on-compact
run "$hooks/on-compact.sh" "$(printf '{"hook_event_name":"SessionStart","source":"startup","session_id":"%s","cwd":"%s"}' "$s" "$root")"
expect_empty "fresh startup is silent" "$out"
run "$hooks/on-compact.sh" "$(printf '{"hook_event_name":"SessionStart","source":"compact","session_id":"%s","cwd":"/tmp"}' "other-session")"
expect_empty "outside a fleet-board checkout is silent" "$out"

gdir="$work/proj"
mkdir -p "$gdir/.agents/skills/fleet-go" "$gdir/.claude"
cp "$root/.agents/skill-paths.json" "$gdir/.agents/"
printf 'SESSION: %s\nCONSENT: push\nShip the dev hooks\n' "$s" >"$gdir/.claude/active-goal.$s"
run "$hooks/on-compact.sh" "$(printf '{"hook_event_name":"SessionStart","source":"compact","session_id":"%s","cwd":"%s"}' "$s" "$gdir")"
expect_rc "on-compact exits 0" 0
ctx="$(json_field "$out" 'd["hookSpecificOutput"]["additionalContext"]')"
expect_contains "points at AGENTS.md" "$ctx" "AGENTS.md"
expect_contains "re-injects the active goal" "$ctx" "Ship the dev hooks"
expect_contains "echoes granted consent" "$ctx" "push"
expect_contains "lists skills that were loaded" "$ctx" "fleet-contract-rigor"
expect_contains "hook event name" "$(json_field "$out" 'd["hookSpecificOutput"]["hookEventName"]')" "SessionStart"
run "$hooks/skill-nudge.sh" "$(edit_payload "$s" "$root/api/fleet/x.proto")"
expect_rc "markers cleared: the same edit is refused again" 2

# --------------------------------------------------------------- goal-continue
stop_payload() { # session cwd active
	printf '{"hook_event_name":"Stop","session_id":"%s","cwd":"%s","stop_hook_active":%s}' "$1" "$2" "$3"
}
g="sess-stop"
gf="$gdir/.claude/active-goal.$g"
printf 'SESSION: %s\nFinish the migration\n' "$g" >"$gf"
run "$hooks/goal-continue.sh" "$(stop_payload "$g" "$gdir" false)"
expect_rc "stop hook exits 0" 0
expect_contains "unfinished goal blocks the stop" "$out" '"decision": "block"'
expect_contains "reason carries the goal" "$(json_field "$out" 'd["reason"]')" "Finish the migration"
expect_contains "reason names arbiter-fleet" "$(json_field "$out" 'd["reason"]')" "arbiter-fleet"

run "$hooks/goal-continue.sh" "$(stop_payload "$g" "$gdir" true)"
expect_empty "stop_hook_active=true allows the stop (loop breaker)" "$out"
run "$hooks/goal-continue.sh" "$(stop_payload "$g" "$gdir" false)" CLAUDE_GOAL_OFF=1
expect_empty "CLAUDE_GOAL_OFF=1 allows the stop" "$out"
run "$hooks/goal-continue.sh" "$(stop_payload "other-session" "$gdir" false)"
expect_empty "another session's goal does not bind this one" "$out"
run "$hooks/goal-continue.sh" "$(stop_payload "$g" "$work/nowhere" false)"
expect_empty "no goal file allows the stop" "$out"
run "$hooks/goal-continue.sh" "garbage"
expect_empty "unparseable payload allows the stop" "$out"

printf 'SESSION: %s\nDONE\nFinish the migration\n' "$g" >"$gf"
run "$hooks/goal-continue.sh" "$(stop_payload "$g" "$gdir" false)"
expect_empty "DONE allows the stop" "$out"

sleep 30 &
live=$!
printf 'SESSION: %s\nBLOCKED: waiting on CI WATCH=%s\nFinish the migration\n' "$g" "$live" >"$gf"
run "$hooks/goal-continue.sh" "$(stop_payload "$g" "$gdir" false)"
expect_empty "BLOCKED with a live watcher allows the stop" "$out"
expect_contains "live watcher is echoed to stderr" "$err" "watcher pid $live"
kill "$live" 2>/dev/null
wait "$live" 2>/dev/null
printf 'SESSION: %s\nBLOCKED: waiting on CI WATCH=%s\nFinish the migration\n' "$g" "$live" >"$gf"
run "$hooks/goal-continue.sh" "$(stop_payload "$g" "$gdir" false)"
expect_contains "BLOCKED with a dead watcher refuses the stop" "$out" "NOT running"
printf 'SESSION: %s\nBLOCKED: waiting on CI WATCH=1\nFinish\n' "$g" >"$gf"
run "$hooks/goal-continue.sh" "$(stop_payload "$g" "$gdir" false)"
expect_contains "WATCH=1 is not a real watcher" "$out" '"decision": "block"'
printf 'SESSION: %s\nBLOCKED: needs a credential OWNER=allen ACTION=add the deploy key\nFinish\n' "$g" >"$gf"
run "$hooks/goal-continue.sh" "$(stop_payload "$g" "$gdir" false)"
expect_empty "BLOCKED with OWNER and ACTION allows the stop" "$out"
printf 'SESSION: %s\nBLOCKED: needs a credential OWNER=allen\nFinish\n' "$g" >"$gf"
run "$hooks/goal-continue.sh" "$(stop_payload "$g" "$gdir" false)"
expect_contains "OWNER without ACTION is refused" "$out" '"decision": "block"'
printf 'SESSION: %s\nBLOCKED: hard question\nFinish\n' "$g" >"$gf"
run "$hooks/goal-continue.sh" "$(stop_payload "$g" "$gdir" false)"
expect_contains "bare BLOCKED is refused and escalated" "$(json_field "$out" 'd["reason"]')" "escalate the question to arbiter-fleet"

# ---------------------------------------------------------------- goal-refresh
prompt_payload() { # session cwd prompt
	printf '{"hook_event_name":"UserPromptSubmit","session_id":"%s","cwd":"%s","prompt":"%s"}' "$1" "$2" "$3"
}
r="sess-refresh"
run "$hooks/goal-refresh.sh" "$(prompt_payload "$r" "$gdir" "/goal consent push, pr-open -- drive fleet-board#12 with fleet-go")"
expect_rc "goal-refresh exits 0" 0
rf="$gdir/.claude/active-goal.$r"
if [ -f "$rf" ] && [ "$(sed -n 1p "$rf")" = "SESSION: $r" ] && [ "$(sed -n 2p "$rf")" = "CONSENT: push, pr-open" ]; then ok; else bad "one-line consent writes SESSION, CONSENT, goal"; fi
ctx="$(json_field "$out" 'd["hookSpecificOutput"]["additionalContext"]')"
expect_contains "first prompt restates the goal" "$ctx" "drive fleet-board#12"
expect_contains "unloaded named skill is nudged" "$ctx" 'load `fleet-go` now'
run "$hooks/skill-loaded.sh" "$(skill_payload "$r" fleet-go)"
run "$hooks/goal-refresh.sh" "$(prompt_payload "$r" "$gdir" "continue")"
ctx="$(json_field "$out" 'd["hookSpecificOutput"]["additionalContext"]')"
expect_contains "later prompts restate the goal" "$ctx" "drive fleet-board#12"
case "$ctx" in *'load `fleet-go` now'*) bad "loaded skill must not be nudged again" ;; *) ok ;; esac
run "$hooks/goal-continue.sh" "$(stop_payload "$r" "$gdir" false)"
expect_contains "goal set by the producer blocks the stop" "$out" '"decision": "block"'
run "$hooks/goal-refresh.sh" "$(prompt_payload "$r" "$gdir" "/goal revoke-consent")"
if grep -q '^CONSENT:' "$rf"; then bad "revoke-consent removes the grant"; else ok; fi
run "$hooks/goal-refresh.sh" "$(prompt_payload "$r" "$gdir" "/goal consent --")"
expect_contains "empty one-line consent is rejected on stderr" "$err" "needs a non-empty"
run "$hooks/goal-refresh.sh" "$(prompt_payload "$r" "$gdir" "/goal done")"
expect_empty "/goal done restates nothing" "$out"
run "$hooks/goal-continue.sh" "$(stop_payload "$r" "$gdir" false)"
expect_empty "retired goal allows the stop" "$out"
run "$hooks/goal-refresh.sh" "$(prompt_payload "$r" "$gdir" "just a prompt")"
expect_empty "retired goal is not restated" "$out"

printf 'hand-written owner goal\n' >"$gdir/.claude/active-goal"
run "$hooks/goal-refresh.sh" "$(prompt_payload "sess-other" "$gdir" "/goal done")"
expect_contains "an unheaded owner file is never retired by a session" "$err" "belongs to another session or to the owner"
if [ "$(sed -n 1p "$gdir/.claude/active-goal")" = "hand-written owner goal" ]; then ok; else bad "owner goal file untouched"; fi
rm -f "$gdir/.claude/active-goal"

# --------------------------------------------------------------- doc-staleness
repo="$work/repo"
mkdir -p "$repo/api/fleet/hub/v1" "$repo/internal/foo" "$repo/docs/specs" "$repo/.agents"
cp "$root/.agents/skill-paths.json" "$repo/.agents/"
(cd "$repo" && git init -q && git config user.email t@example.com && git config user.name t &&
	: >docs/specs/02-contracts.md && : >docs/specs/02b-hub-api.md && : >docs/specs/ui-contract-map.md &&
	git add -A && git commit -q -m base)
printf 'syntax = "proto3";\n' >"$repo/api/fleet/hub/v1/x.proto"
printf 'package foo\n' >"$repo/internal/foo/foo.go"
run "$hooks/doc-staleness.sh" "$(printf '{"hook_event_name":"PostToolUse","session_id":"docs","cwd":"%s"}' "$repo")"
expect_rc "doc-staleness exits 0" 0
ctx="$(json_field "$out" 'd["hookSpecificOutput"]["additionalContext"]')"
expect_contains "missing doc.go reported" "$ctx" "internal/foo: missing doc.go"
expect_contains "missing README reported" "$ctx" "internal/foo: missing README.md"
expect_contains "proto change names 02-contracts.md" "$ctx" "docs/specs/02-contracts.md"
expect_contains "proto change names 02b-hub-api.md" "$ctx" "docs/specs/02b-hub-api.md"
expect_contains "proto change names ui-contract-map.md" "$ctx" "docs/specs/ui-contract-map.md"
run "$hooks/doc-staleness.sh" "$(printf '{"hook_event_name":"PostToolUse","session_id":"docs","cwd":"%s"}' "$repo")"
expect_empty "unchanged reminder is not repeated" "$out"
printf 'a\n' >"$repo/docs/specs/02-contracts.md"
printf 'a\n' >"$repo/docs/specs/02b-hub-api.md"
printf 'a\n' >"$repo/docs/specs/ui-contract-map.md"
printf '// Package foo is a test package.\npackage foo\n' >"$repo/internal/foo/doc.go"
printf '# foo\n' >"$repo/internal/foo/README.md"
printf '# foo rules\n' >"$repo/internal/foo/AGENTS.md"
sleep 1
touch "$repo/internal/foo/doc.go" "$repo/internal/foo/README.md" "$repo/internal/foo/AGENTS.md"
run "$hooks/doc-staleness.sh" "$(printf '{"hook_event_name":"PostToolUse","session_id":"docs2","cwd":"%s"}' "$repo")"
expect_empty "docs updated with the change: no reminder" "$out"

# ------------------------------------------------------- harness wiring (real)
# Run each config's own command string, the way the harness would.
cfg_command() { # file event matcher
	python3 - "$1" "$2" "$3" <<'PY'
import json, sys
cfg = json.load(open(sys.argv[1]))["hooks"][sys.argv[2]]
for row in cfg:
    if row.get("matcher", "") == sys.argv[3]:
        print(row["hooks"][0]["command"]); break
PY
}
wire() { # name config event matcher payload [env-mode]
	local cmd
	cmd="$(cfg_command "$root/$2" "$3" "$4")"
	if [ -z "$cmd" ]; then bad "$1: no $3/$4 command in $2"; out=""; err=""; rc=99; return; fi
	out="$(cd "$root" && printf '%s' "$5" | env -i HOME="$HOME" PATH="$PATH" FLEET_HOOK_STATE_DIR="$FLEET_HOOK_STATE_DIR" ${6:-} /bin/sh -c "$cmd" 2>"$work/stderr")"
	rc=$?
	err="$(cat "$work/stderr")"
}
w="sess-wire"
wire "claude wiring: Edit|MultiEdit|Write nudge" .claude/settings.json PreToolUse "Edit|MultiEdit|Write" "$(edit_payload "$w" "$root/api/fleet/x.proto")" "CLAUDE_PROJECT_DIR=$root"
expect_rc "claude settings run the nudge" 2
wire "muse wiring: write_file nudge, cleared env" .muse/hooks.json PreToolUse write_file "$(edit_payload "$w" "$root/api/fleet/x.proto")"
expect_rc "muse hooks run the nudge with a cleared environment" 2
wire "muse wiring: edit_file nudge" .muse/hooks.json PreToolUse edit_file "$(edit_payload "$w" "$root/api/fleet/x.proto")"
expect_rc "muse edit_file matcher runs the nudge" 2
wire "muse wiring: read_skill" .muse/hooks.json PostToolUse read_skill "$(printf '{"session_id":"%s","cwd":"%s","tool_input":{"name":"fleet-contract-rigor"}}' "$w" "$root")"
expect_rc "muse read_skill records the skill (tool_input.name)" 0
wire "muse wiring: nudge after read_skill" .muse/hooks.json PreToolUse write_file "$(edit_payload "$w" "$root/api/fleet/x.proto")"
expect_rc "muse edit passes after read_skill" 0
wire "muse wiring: Stop" .muse/hooks.json Stop "" "$(printf '{"hook_event_name":"Stop","session_id":"%s","cwd":"%s","stop_hook_active":false}' "$g" "$gdir")"
expect_rc "muse Stop hook exits 0 with no goal here" 0
wire "claude wiring: Stop" .claude/settings.json Stop "" "$(stop_payload "$g" "$gdir" false)" "CLAUDE_PROJECT_DIR=$root"
expect_rc "claude Stop hook exits 0" 0
wire "codex wiring: apply_patch doc-staleness" .codex/hooks.json PostToolUse '^apply_patch$' '{"session_id":"cx","cwd":"/tmp"}' "CODEX_PROJECT_DIR=$root"
expect_rc "codex doc-staleness exits 0 outside a fleet tree" 0

# ---------------------------------------------------------------- python suites
if python3 "$root/scripts/test-goal-role-router.py" >"$work/py1" 2>&1; then ok; else bad "test-goal-role-router.py"; cat "$work/py1" >&2; fi
if python3 "$root/scripts/test-codex-spawn-guard.py" >"$work/py2" 2>&1; then ok; else bad "test-codex-spawn-guard.py"; cat "$work/py2" >&2; fi
if python3 "$root/scripts/test-check-attribution.py" >"$work/py3" 2>&1; then ok; else bad "test-check-attribution.py"; cat "$work/py3" >&2; fi
if python3 "$root/scripts/test-check-file-cap.py" >"$work/py4" 2>&1; then ok; else bad "test-check-file-cap.py"; cat "$work/py4" >&2; fi
if python3 "$root/scripts/test-plan-issues.py" >"$work/py5" 2>&1; then ok; else bad "test-plan-issues.py"; cat "$work/py5" >&2; fi

printf 'test-dev-hooks: %s passed, %s failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
