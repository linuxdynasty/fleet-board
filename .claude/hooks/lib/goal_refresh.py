#!/usr/bin/env python3
"""UserPromptSubmit hook logic: set, amend and restate the session goal.

stdin : {session_id, cwd, prompt, hook_event_name: "UserPromptSubmit", ...}
stdout: {"hookSpecificOutput": {"hookEventName": "UserPromptSubmit",
         "additionalContext": "..."}} or nothing
exit  : always 0; this runs on every prompt and must never stop one sending.

Producer (the single write path; there is no fuzzy goal inference):
  /goal <text>  |  GOAL: <text>          set this session's goal
  /goal done | clear                     retire it (DONE is prepended)
  /goal consent <acts>                   replace the CONSENT: line
  /goal consent <acts> -- <text>         start a goal with the grant attached
  /goal revoke-consent                   remove the CONSENT: line
Consent and retire edit only a file this session owns: its own write target or
a file whose SESSION: header names it. An unheaded file is the owner's and is
never amended. Nothing is written to the machine-wide ~/.claude/active-goal.
A rejection says so on stderr: a consent that silently goes nowhere is the loop
this feature exists to close.

Refresher: on every prompt the active goal is restated through
additionalContext so it survives long sessions and compaction, with a nudge to
load any project skill the goal names but this session has not loaded.

Codex does not fire UserPromptSubmit for /goal lines (docs/specs/01d), so under
Codex only a plain `GOAL: <text>` prompt reaches this hook, which Codex does
not wire; it is wired for Claude Code and Muse.
"""
from __future__ import annotations

import importlib.util
import json
import os
import re
import sys
from pathlib import Path

import goal_lib

PREFIX = r"^(?:/goal|GOAL:)\s+"
ROUTER = Path(__file__).resolve().parents[3] / "scripts" / "goal-role-router.py"


def warn(message):
    print("goal-refresh: " + message, file=sys.stderr)


def expand(text, cwd):
    """Read a prepared goal file named by /goal <path>, via the role router."""
    try:
        spec = importlib.util.spec_from_file_location("goal_role_router", ROUTER)
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        return module.prepared_goal(text, cwd) or text
    except Exception:
        return text


def owns(path, target, sid):
    if Path(path) == Path(target):
        return True
    header = goal_lib.parse(goal_lib.read_text(path))[0]
    return header == sid


def writable(path, target, sid, action):
    """None when this session may amend the file, else the refusal reason."""
    home_goal = Path(os.environ.get("HOME", "/nonexistent")) / ".claude" / "active-goal"
    if path is None or not Path(path).is_file():
        return "no goal file for this session, %s not recorded" % action
    if not os.environ.get("CLAUDE_GOAL_FILE") and Path(path) == home_goal:
        return "refusing to write %s into the machine-wide goal file %s" % (action, path)
    if not owns(path, target, sid):
        return "%s belongs to another session or to the owner, %s not recorded" % (path, action)
    return None


def rebuild(header, acts, body):
    lines = ["SESSION: " + header] if header is not None else []
    if acts:
        lines.append("CONSENT: " + acts)
    return "\n".join(lines + [body]) + "\n"


def new_goal(sid, target, text, acts=""):
    goal_lib.write_atomic(target, rebuild(sid, acts, text))
    return target


def produce(prompt, sid, cwd, target, found):
    """Handle a /goal command. Returns (handled_and_done, goal_file_to_restate)."""
    if re.match(PREFIX + r"(?:done|clear)(?:\s|$)", prompt):
        path = found or target
        problem = writable(path, target, sid, "retirement")
        if problem:
            warn(problem)
        else:
            goal_lib.write_atomic(path, "DONE\n" + goal_lib.read_text(path))
        return True, None
    if re.match(PREFIX + r"revoke-consent(?:\s|$)", prompt):
        problem = writable(found, target, sid, "consent")
        if problem:
            warn(problem)
        else:
            header, _, body = goal_lib.parse(goal_lib.read_text(found))
            goal_lib.write_atomic(found, rebuild(header, "", body))
        return True, None
    consent = re.match(PREFIX + r"consent(?:\s+(.*))?$", prompt, re.S)
    if consent:
        rest = (consent.group(1) or "").strip()
        padded = " " + rest + " "
        cut = padded.find(" -- ")
        if cut < 0:
            problem = writable(found, target, sid, "consent")
            if problem:
                warn(problem)
            else:
                header, _, body = goal_lib.parse(goal_lib.read_text(found))
                goal_lib.write_atomic(found, rebuild(header, rest, body))
            return True, None
        acts, text = padded[:cut].strip(), padded[cut + 4:].strip()
        if not acts or not text:
            warn("/goal consent <acts> -- <goal text> needs a non-empty acts list and goal; nothing written.")
            return True, None
        if target is None:
            return True, None
        return False, new_goal(sid, target, expand(text, cwd), acts)
    goal = re.match(PREFIX + r"(.+)$", prompt, re.S)
    if goal and target is not None:
        text = expand(goal.group(1).strip(), cwd)
        if text:
            return False, new_goal(sid, target, text)
    return False, None


def consent_note(acts):
    if not acts:
        return (" Irreversible acts (push, merge, release, delete, data change) get an arbiter-fleet ruling "
                "first; do not stop to ask the owner.")
    granted = ", ".join(acts)
    blanket = any(t.strip().lower() in ("all", "*") for a in acts for t in a.split(","))
    return ("\n\nOWNER CONSENT ALREADY GRANTED%s for: %s. Carry those out yourself when the work reaches them; "
            "do not stop to ask again for anything on that list. An irreversible act NOT on it gets an "
            "arbiter-fleet ruling first." % (", blanket," if blanket else "", granted))


def main():
    payload = json.load(sys.stdin)
    if not isinstance(payload, dict) or os.environ.get("CLAUDE_GOAL_OFF") == "1":
        return
    sid = payload.get("session_id")
    if not isinstance(sid, str) or not sid:
        return
    try:
        saved = re.sub(r"[^A-Za-z0-9._-]", "-", sid)[:12] or "na"
        os.unlink(os.path.join(os.environ.get("FLEET_HOOK_STATE_DIR") or os.environ.get("TMPDIR") or "/tmp",
                               "fleet-stop-blocks-" + saved))
    except OSError:
        pass
    cwd = str(payload.get("cwd") or "")
    prompt = str(payload.get("prompt") or "").lstrip()
    target = goal_lib.write_target(sid, cwd)
    found = goal_lib.find_file(sid, cwd)
    if re.match(PREFIX, prompt):
        finished, written = produce(prompt, sid, cwd, target, found)
        if finished:
            return
    goal = goal_lib.active_goal(sid, cwd)
    if goal is None or goal.done:
        return
    acts = goal.consent + goal_lib.env_consent()
    note = ("ACTIVE GOAL (restated each turn so it cannot go stale):\n" + goal_lib.clip(goal.body, goal.path)
            + "\n\nKeep working it. Take the next concrete action yourself. Do not ask the owner something the "
              "code, a local doc, a loaded skill or an arbiter-fleet ruling can settle." + consent_note(acts))
    missing = goal_lib.missing_named_skills(goal.body, goal_lib.project_root(cwd), sid)
    if missing:
        note += "\n\n" + " ".join("load `%s` now" % m for m in missing)
    print(json.dumps({"hookSpecificOutput": {"hookEventName": "UserPromptSubmit", "additionalContext": note}}))


if __name__ == "__main__":
    try:
        main()
    except Exception:  # fail open
        pass
    sys.exit(0)
