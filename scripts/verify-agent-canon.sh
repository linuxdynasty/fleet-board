#!/usr/bin/env bash
#
# verify-agent-canon.sh: fail if the shared agent setup drifts or conflicts.
#
# Checks, in order:
#   1. AGENTS.md exists, stays within its line budget, and is the only agent
#      canon: no CLAUDE.md anywhere. Claude Code reads AGENTS.md natively; a
#      CLAUDE.md makes it read that file INSTEAD for its directory tree and
#      hides every scoped AGENTS.md below it.
#   2. The rendered role files match .agents/roles.json (agent-roles.py check).
#   3. Every .agents/skills/<name>/SKILL.md has valid frontmatter (name equals
#      the directory, a quoted description starting with "Use when"), is within
#      the file cap, and is indexed in AGENTS.md.
#   4. .claude/skills and .codex/skills hold exactly one correct symlink per
#      skill and nothing else.
#   5. Every fleet-* skill named in roles.json exists.
#   6. Agent-facing files stay under the 500-line cap.
#   7. .agents/skill-paths.json (the path-to-skill map the skill-nudge hook
#      reads) is valid, names only existing skills, and maps or exempts every
#      skill.
#   8. The three hook configs (.claude/settings.json, .codex/hooks.json,
#      .muse/hooks.json) are valid JSON and reference only scripts that exist;
#      hook scripts parse (bash -n, python ast) and the shell ones are
#      executable.
#
# Portable to the macOS system bash 3.2 (no associative arrays, no mapfile), so
# `#!/usr/bin/env bash` works with either /bin/bash or Homebrew bash. Needs
# python3, grep, awk, find, cmp.
#
# FLEET_AGENT_CANON_REPO_ROOT points the script at another tree (used to test
# the checks against a seeded violation).
set -euo pipefail

root="${FLEET_AGENT_CANON_REPO_ROOT:-}"
if [ -z "$root" ]; then
  # The script lives at <repo>/scripts/; deriving the root from its own path is
  # worktree- and hook-safe (git hooks export GIT_DIR).
  root="$(cd "$(dirname "$0")/.." && pwd)"
fi

fail() {
  printf 'verify-agent-canon: %s\n' "$*" >&2
  exit 1
}

max_agents_lines=200
max_file_lines=500

# 1. AGENTS.md is the one canon.
agents="$root/AGENTS.md"
[ -f "$agents" ] || fail "missing required file $agents"
agents_lines="$(wc -l < "$agents" | tr -d ' ')"
if [ "$agents_lines" -gt "$max_agents_lines" ]; then
  fail "AGENTS.md has $agents_lines lines (budget $max_agents_lines); move detail into docs/development/"
fi

shadows="$(find "$root" \( -name .git -o -name node_modules -o -name fleet-board-worktrees \) -prune -o -name 'CLAUDE.md' -type f -print 2>/dev/null || true)"
if [ -n "$shadows" ]; then
  printf 'verify-agent-canon: CLAUDE.md files found:\n%s\n' "$shadows" >&2
  fail "Claude Code reads AGENTS.md natively; a CLAUDE.md hides every scoped AGENTS.md. Move its content into AGENTS.md and delete it."
fi
printf 'verify-agent-canon: AGENTS.md is the only agent canon (%s lines, no CLAUDE.md).\n' "$agents_lines"

# 2. Rendered role files match the manifest.
[ -f "$root/.agents/roles.json" ] || fail "missing .agents/roles.json"
[ -f "$root/scripts/agent-roles.py" ] || fail "missing scripts/agent-roles.py"
FLEET_AGENT_ROLES_REPO_ROOT="$root" python3 "$root/scripts/agent-roles.py" check

# 3 and 4. Skills: frontmatter, size, index, discovery links.
skills_root="$root/.agents/skills"
[ -d "$skills_root" ] || fail "missing $skills_root"
count=0
for skill_file in "$skills_root"/*/SKILL.md; do
  [ -f "$skill_file" ] || continue
  count=$((count + 1))
  skill_name="$(basename "$(dirname "$skill_file")")"

  first_line="$(head -n 1 "$skill_file")"
  [ "$first_line" = "---" ] || fail "$skill_file must start with --- frontmatter"
  fm_name="$(awk 'NR==1{next} $0=="---"{exit} /^name: /{sub(/^name: /,""); print}' "$skill_file")"
  fm_desc="$(awk 'NR==1{next} $0=="---"{exit} /^description: /{sub(/^description: /,""); print}' "$skill_file")"
  [ "$fm_name" = "$skill_name" ] || fail "$skill_file: name must equal the directory ($skill_name)"
  [ -n "$fm_desc" ] || fail "$skill_file: description must be set"
  case "$fm_desc" in
    \"Use\ when\ *\") ;;
    *) fail "$skill_file: description must be a double-quoted string starting with \"Use when\"" ;;
  esac

  lines="$(wc -l < "$skill_file" | tr -d ' ')"
  [ "$lines" -le "$max_file_lines" ] || fail "$skill_file has $lines lines (cap $max_file_lines)"

  if ! grep -q -F "$skill_name" "$agents"; then
    fail "AGENTS.md does not index skill $skill_name"
  fi

  for harness in .claude .codex; do
    link="$root/$harness/skills/$skill_name"
    [ -L "$link" ] || fail "$harness cannot discover skill $skill_name; missing symlink $link"
    target="$(readlink "$link")"
    [ "$target" = "../../.agents/skills/$skill_name" ] || fail "$link points to $target, expected ../../.agents/skills/$skill_name"
    if [ ! -f "$link/SKILL.md" ] || ! cmp -s "$skill_file" "$link/SKILL.md"; then
      fail "$link does not resolve to $skill_file"
    fi
  done
done
[ "$count" -gt 0 ] || fail "no skills found under $skills_root"

# Nothing in the discovery directories that is not a skill symlink.
for harness in .claude .codex; do
  for entry in "$root/$harness/skills"/*; do
    [ -e "$entry" ] || [ -L "$entry" ] || continue
    name="$(basename "$entry")"
    [ -L "$entry" ] || fail "$entry is not a symlink; skills live only in .agents/skills"
    [ -f "$skills_root/$name/SKILL.md" ] || fail "$entry has no matching .agents/skills/$name/SKILL.md"
  done
done
printf 'verify-agent-canon: %s skills have valid frontmatter, are indexed, and are linked for Claude and Codex.\n' "$count"

# 5. Every fleet-* skill named in the manifest exists (the generator checks the
# "skill" field; this also catches names mentioned in instructions).
# A token counts only when not preceded by a letter or hyphen, so role names
# such as scan-fleet or develop-fleet-deep are not read as skills.
for name in $(grep -oE '(^|[^A-Za-z-])fleet-[a-z][a-z-]*' "$root/.agents/roles.json" | sed -E 's/^[^f]*//' | sort -u); do
  [ "$name" = "fleet-board" ] && continue
  [ -f "$skills_root/$name/SKILL.md" ] || fail "roles.json names skill $name, which does not exist"
done
printf 'verify-agent-canon: every skill named in roles.json exists.\n'

# 6. File cap on agent-facing files.
over=""
for f in "$agents" "$root"/docs/development/*.md "$root"/.agents/roles.json "$root"/.agents/skill-paths.json \
  "$root"/scripts/*.py "$root"/scripts/*.sh "$root"/.claude/settings.json "$root"/.codex/hooks.json \
  "$root"/.muse/hooks.json "$root"/.claude/hooks/*.sh "$root"/.claude/hooks/lib/*; do
  [ -f "$f" ] || continue
  n="$(wc -l < "$f" | tr -d ' ')"
  if [ "$n" -gt "$max_file_lines" ]; then
    over="$over $f($n)"
  fi
done
[ -z "$over" ] || fail "files over the $max_file_lines-line cap:$over"
printf 'verify-agent-canon: agent-facing files are within the %s-line cap.\n' "$max_file_lines"

# 7. The path-to-skill map. skill_paths.py check validates the JSON, every skill
# it names (and every exemption) exists, and no skill is unrouted.
skill_paths="$root/.claude/hooks/lib/skill_paths.py"
[ -f "$root/.agents/skill-paths.json" ] || fail "missing .agents/skill-paths.json"
[ -f "$skill_paths" ] || fail "missing $skill_paths"
problems="$(python3 "$skill_paths" check "$root")" || fail "skill path map problems:
$problems"
printf 'verify-agent-canon: .agents/skill-paths.json names only existing skills and routes every skill.\n'

# 8. Hook configs reference scripts that exist, and the scripts parse.
for config in .claude/settings.json .codex/hooks.json .muse/hooks.json; do
  [ -f "$root/$config" ] || fail "missing hook config $config"
done
problems="$(python3 - "$root" <<'PY'
import json, re, sys
from pathlib import Path

root = Path(sys.argv[1])
ref = re.compile(r"(?:\.claude/hooks|\.codex/hooks|\.muse/hooks|scripts)/[A-Za-z0-9_./-]+\.(?:sh|py)")
for config in (".claude/settings.json", ".codex/hooks.json", ".muse/hooks.json"):
    try:
        hooks = json.loads((root / config).read_text())["hooks"]
    except (ValueError, KeyError) as err:
        print("%s is not valid hook JSON: %s" % (config, err))
        continue
    count = 0
    for event, rows in hooks.items():
        for row in rows:
            for hook in row.get("hooks", []):
                command = hook.get("command", "")
                if hook.get("type") != "command" or not command:
                    print("%s %s: every hook needs type command and a command" % (config, event))
                    continue
                count += 1
                found = ref.findall(command)
                if not found:
                    print("%s %s: command references no repo script" % (config, event))
                for path in found:
                    if not (root / path).is_file():
                        print("%s %s: references %s, which does not exist" % (config, event, path))
    if count == 0:
        print("%s declares no hooks" % config)
PY
)" || fail "hook config check could not run"
[ -z "$problems" ] || fail "hook config problems:
$problems"
hook_scripts=0
for script in "$root"/.claude/hooks/*.sh "$root"/.claude/hooks/lib/*.sh "$root"/scripts/test-dev-hooks.sh; do
  [ -f "$script" ] || continue
  hook_scripts=$((hook_scripts + 1))
  bash -n "$script" || fail "$script does not parse (bash -n)"
done
for script in "$root"/.claude/hooks/*.sh "$root"/scripts/test-dev-hooks.sh; do
  [ -x "$script" ] || fail "$script is not executable"
done
for script in "$root"/.claude/hooks/lib/*.py "$root"/scripts/*.py; do
  [ -f "$script" ] || continue
  hook_scripts=$((hook_scripts + 1))
  python3 -c 'import ast, sys; ast.parse(open(sys.argv[1]).read())' "$script" || fail "$script does not parse (python)"
done
printf 'verify-agent-canon: three hook configs are valid JSON and reference existing scripts; %s hook and test scripts parse.\n' "$hook_scripts"
