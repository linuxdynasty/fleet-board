#!/usr/bin/env python3
"""Add manifest-backed role routing context to explicit fleet-board goal prompts.

Reads a UserPromptSubmit (or Agent PreToolUse) payload on stdin and the roles
in .agents/roles.json. Modes (argv[1]):

  claude | codex | muse   a /goal or GOAL: prompt that names project skills gets
                          role routing and, for issue drives, the drive contract
                          with the tier's model binding for that harness
  expand                  print the goal text, reading a prepared goal.txt or
                          goal.md named by `/goal <path>` (used by goal-refresh)
  claude-dispatch         PreToolUse on Agent: drop a model override that
                          differs from the named role's frontmatter model,
                          unless the active goal names a model itself

Hooks fail quiet: any error prints nothing and exits 0, so a prompt is never
blocked by routing.
"""
from __future__ import annotations

import json
import re
import sys
import tempfile
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
SKILL_NAMES = sorted(
    (path.parent.name for path in (ROOT / ".agents/skills").glob("*/SKILL.md")),
    key=len,
    reverse=True,
)
SKILL_PATTERN = re.compile(
    r"(?<![\w-])(?:" + "|".join(map(re.escape, SKILL_NAMES)) + r")(?![\w-])"
)
CONTROL_WORDS = {"done", "clear", "consent", "revoke-consent"}


def prepared_goal(body: str, cwd: str) -> str:
    """The text of a goal.txt/goal.md inside the workspace or a Claude scratchpad."""
    if len(body) >= 1024 or "\n" in body:
        return ""
    candidate = Path(body)
    if candidate.name not in {"goal.txt", "goal.md"} or not cwd:
        return ""
    workspace = Path(cwd).resolve()
    if not candidate.is_absolute():
        candidate = workspace / candidate
    if candidate.is_symlink() or not candidate.is_file() or candidate.stat().st_size > 65536:
        return ""
    resolved = candidate.resolve()
    in_workspace = resolved.is_relative_to(workspace)
    slug = str(workspace).replace("/", "-")
    temp_roots = {Path(tempfile.gettempdir()).resolve(), Path("/private/tmp").resolve()}
    in_claude_scratchpad = (
        len(resolved.parents) >= 5
        and resolved.parents[0].name == "scratchpad"
        and resolved.parents[2].name == slug
        and resolved.parents[3].name.startswith("claude-")
        and any(resolved.is_relative_to(root) for root in temp_roots)
    )
    if in_workspace or in_claude_scratchpad:
        return resolved.read_text(errors="replace")
    return ""


def goal_text(prompt: str, cwd: str = "") -> str:
    stripped = prompt.strip()
    if stripped.startswith("/goal "):
        body = stripped[6:].strip()
    elif stripped.startswith("GOAL:"):
        body = stripped[5:].strip()
    else:
        return ""
    if not body or body.split()[0] in CONTROL_WORDS:
        return ""
    return prepared_goal(body, cwd) or body


def resolve(manifest: dict, name: str) -> dict:
    role = manifest["roles"][name]
    if "base" in role:
        role = {**manifest["roles"][role["base"]], **role}
    return role


def is_drive(skills: set, body: str) -> bool:
    """Only an explicit driver skill or an opening drive + issue id opts in."""
    normalized = " ".join(body.lower().split())
    invokes_driver = "fleet-issue-driver" in skills and bool(re.search(
        r"(?:^(?:\$)?|\b(?:using|with|use|follow|apply|load|invoke|run)\s+\$?)"
        r"fleet-issue-driver\b", normalized
    ))
    return invokes_driver or bool(
        re.match(r"drive\s+(?:(?:issues?|epics?)\s+)?#\d+\b", normalized)
    )


def pick_roles(skills: set, lower: str, drive: bool = False) -> list:
    deep = bool(re.search(r"\b(intermittent|cross.system|difficult|deep diagnosis|deep performance)\b", lower))
    pair = lambda base: base + "-deep" if deep else base  # noqa: E731
    roles = []
    if re.search(r"\b(scan|inventory|gather evidence)\b", lower):
        roles.append("scan-fleet")
    if "fleet-diagnostic-rigor" in skills or re.search(r"\b(debug|diagnos|root cause|reproduc)", lower):
        roles.append(pair("debug-fleet"))
    if "fleet-performance-rigor" in skills or re.search(r"\b(benchmark|profil|bottleneck|latency)", lower):
        roles.append(pair("perf-fleet"))
    if re.search(r"\b(implement|fix|patch|code|apply|tighten|refactor|migrate)\b|\b(update|change|add|write|build)\s+(?:code|tests?|feature|schema|implementation)\b", lower):
        roles.append(pair("develop-fleet"))
    if "fleet-code-review" in skills or re.search(r"\b(review|pr.readiness)\b", lower):
        roles.append(pair("review-fleet"))
    if re.search(r"\b(arbiter|arbitrat\w*|design[- ](?:call|fork)|waiver|ruling)\b", lower):
        roles.append("arbiter-fleet")
    if "fleet-go" in skills and not roles:
        roles.append("develop-fleet")
    if drive:
        roles.extend((pair("develop-fleet"), pair("review-fleet")))
    return list(dict.fromkeys(roles)) or ["scan-fleet"]


def route(prompt: str, harness: str, cwd: str = "") -> str:
    body = goal_text(prompt, cwd)
    if not body or not SKILL_NAMES:
        return ""
    manifest = json.loads((ROOT / ".agents/roles.json").read_text())
    skills = set(SKILL_PATTERN.findall(body.lower()))
    drive = is_drive(skills, body)
    if drive:
        skills.add("fleet-issue-driver")
    if not skills:
        return ""
    roles = pick_roles(skills, SKILL_PATTERN.sub(" ", body.lower()), drive)
    lines = ["fleet-board goal role hints (phase candidates, not an execution order):"]
    if drive:
        lines.append("- fleet-issue-driver stays with the main coordinator for issue/PR ownership.")
    bindings = manifest["models"].get(harness, {})
    for name in dict.fromkeys(roles):
        role = resolve(manifest, name)
        binding = bindings.get(role["tier"])
        model = "; %s effort=%s" % (binding["model"], binding["effort"]) if binding else ""
        lines.append("- %s: %s (%s; %s%s)" % (name, role["description"], role["tier"], role["access"], model))
    if harness == "claude":
        lines.append(
            "When delegating a bounded phase, select its named .claude/agents agent type "
            "and omit a model override unless the goal requests one. Team teammates "
            "must load named skills explicitly."
        )
    elif harness in {"codex", "muse"}:
        lines.append("When the native child tool cannot select this role and binding, the coordinator can run "
                     "scripts/agent-roles.py %s-exec ROLE TASK." % harness)
    if drive:
        fanout = {"claude": "native Agent subagents or team teammates",
                  "muse": "native subagents/workflows when their bindings are qualified; otherwise muse-exec"}.get(
                      harness, "native named subagents; otherwise codex-exec")
        lines.append(
            "DRIVE CONTRACT: load fleet-issue-driver now. The coordinator owns the issue inventory, "
            "dependencies, PRs and authorized endpoint. Decompose epics into deduplicated leaves, "
            "check what main already satisfies, and state the execution order. Delegate independent "
            "leaves through " + fanout + "; keep shared-file or dependent work sequential and record "
            "why. Route implementation and independent review to the named roles above; commit before "
            "review dispatch. Each child loads its named skills and receives its worktree, owned files, "
            "acceptance criteria and manifest binding. Only the coordinator runs the full gate and "
            "publishes. Start or refresh a PR watcher in the same turn as PR creation or a follow-up "
            "push; cover checks, conflicts and comments from every author at the current head. "
            "Track authorized new issues in the goal inventory with existing labels and an epic link "
            "when one fits. The owner merges every v1 PR. Stop at the owner's authorized endpoint; "
            "this contract grants no permission to publish, merge or expand scope."
        )
    if harness == "muse":
        lines.append(
            "Muse dispatch: load each named skill body before working on its surface. Delegate "
            "independent lanes, or record why inline is correct. Use the manifest model and effort "
            "for each role; use muse-exec when native role selection/bindings have not been qualified "
            "for the installed version. Never infer working hooks or model enforcement from file "
            "discovery alone. Follow fleet-issue-driver's PR watcher and follow-up inventory rules "
            "when driving issues."
        )
    lines.append("Preserve the goal's explicit phases and model choices. Do not assign the whole goal from this "
                 "hint; the main session model is unchanged.")
    return "\n".join(lines)


def claude_dispatch(payload: dict) -> str:
    """Keep a named role's frontmatter model for an ordinary /goal run."""
    if payload.get("tool_name") != "Agent":
        return ""
    tool_input = payload.get("tool_input")
    if not isinstance(tool_input, dict) or not isinstance(tool_input.get("model"), str):
        return ""
    role_name = tool_input.get("subagent_type")
    manifest = json.loads((ROOT / ".agents/roles.json").read_text())
    if not isinstance(role_name, str) or role_name not in manifest["roles"]:
        return ""
    expected = manifest["models"]["claude"][resolve(manifest, role_name)["tier"]]["model"]
    if tool_input["model"] == expected:
        return ""
    cwd, session_id = payload.get("cwd"), payload.get("session_id")
    if not isinstance(cwd, str) or not isinstance(session_id, str) or not cwd or not session_id:
        return ""
    goal_file = Path(cwd) / ".claude" / ("active-goal." + re.sub(r"[^A-Za-z0-9._-]", "-", session_id))
    if goal_file.is_symlink() or not goal_file.is_file() or goal_file.stat().st_size > 65536:
        return ""
    goal_lines = goal_file.read_text(errors="replace").splitlines()
    start = 0
    while start < len(goal_lines) and (
        not goal_lines[start].strip() or goal_lines[start].lstrip().lower().startswith("consent:")
    ):
        start += 1
    if start == len(goal_lines) or goal_lines[start] != "SESSION: " + session_id:
        return ""
    goal = "\n".join(goal_lines[start + 1:])
    if re.search(r"(?im)^\s*DONE\b", goal):
        return ""
    goal = prepared_goal(goal.strip(), cwd) or goal
    # A model the owner named in the goal takes precedence over repo defaults.
    if re.search(r"(?i)\b(?:haiku|sonnet|opus|fable|claude-(?:haiku|sonnet|opus|fable)[\w.-]*)\b", goal):
        return ""
    revised = {key: value for key, value in tool_input.items() if key != "model"}
    return json.dumps({"hookSpecificOutput": {
        "hookEventName": "PreToolUse",
        "updatedInput": revised,
        "additionalContext": "fleet-board removed an unrequested model override for %s; the role frontmatter "
                             "selects %s." % (role_name, expected),
    }})


def main() -> None:
    try:
        payload = json.load(sys.stdin)
        harness = sys.argv[1] if len(sys.argv) > 1 else ""
        prompt = str(payload.get("prompt", ""))
        cwd = str(payload.get("cwd", ""))
        if harness == "claude-dispatch":
            result = claude_dispatch(payload)
        elif harness == "expand":
            result = goal_text(prompt, cwd)
        else:
            context = route(prompt, harness, cwd)
            result = json.dumps({"hookSpecificOutput": {
                "hookEventName": "UserPromptSubmit", "additionalContext": context}}) if context else ""
        if result:
            print(result)
    except (OSError, ValueError, TypeError, KeyError, AttributeError):
        # Prompt hooks must not prevent the user's message from being sent.
        return


if __name__ == "__main__":
    main()
