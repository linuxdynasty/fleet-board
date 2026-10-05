#!/usr/bin/env python3
"""Run a fleet-board dev hook script from a native Cursor hook.

Usage: cursor-hook.py <event> <hook-script> [args...]

Cursor (IDE and cursor-agent) reads .cursor/hooks.json. Each command there runs
this adapter with the Cursor event name and the existing hook script. The
adapter reads the Cursor payload on stdin, hands the script the Claude-shaped
payload it already understands, and turns its exit code and output back into
what Cursor expects. The scripts stay the one home of the logic; the mapping
and the reasons some hooks do not port are in docs/development/hooks.md.

Events: beforeShellExecution, preToolUse, afterFileEdit, postToolUse,
sessionStart, preCompact, beforeSubmitPrompt, stop.

Failure modes. A guard is a hook that can block (beforeShellExecution,
preToolUse): when the script is missing, crashes, times out or the payload is
unreadable, it fails CLOSED with a deny that names the cause. Every other hook
fails open with a note on stderr. A guard always prints exactly one JSON object,
because Cursor treats invalid JSON on a permission hook as a block.

No network, standard library only. The payload is data: a file path from it only
ever becomes a field of the payload handed to the script.
"""
from __future__ import annotations

import json
import os
import re
import subprocess
import sys

GUARDS = {"beforeShellExecution", "preToolUse"}
EVENTS = GUARDS | {"afterFileEdit", "postToolUse", "sessionStart", "preCompact", "beforeSubmitPrompt", "stop"}
# Cursor does not document the field names inside a Write tool_input.
PATH_KEYS = ("file_path", "path", "target_file", "filePath", "file")
SKILL_FILE = re.compile(r"(?:^|/)skills/([A-Za-z0-9][A-Za-z0-9._-]*)/SKILL\.md$")
TIMEOUT_SECONDS = 8


class Failure(Exception):
    """The hook script could not be run, or did not finish."""


def note(message):
    print("cursor-hook: " + message, file=sys.stderr)


def project_dir(payload):
    roots = payload.get("workspace_roots")
    first = roots[0] if isinstance(roots, list) and roots and isinstance(roots[0], str) else ""
    return os.environ.get("CURSOR_PROJECT_DIR") or os.environ.get("CLAUDE_PROJECT_DIR") or first or os.getcwd()


def edited_path(payload):
    tool_input = payload.get("tool_input")
    if isinstance(tool_input, dict):
        for key in PATH_KEYS:
            if isinstance(tool_input.get(key), str) and tool_input[key]:
                return tool_input[key]
    value = payload.get("file_path")
    return value if isinstance(value, str) else ""


def claude_payload(event, payload, cwd):
    """The payload the existing script expects, or None when there is nothing to run."""
    base = {"cwd": cwd, "session_id": str(payload.get("conversation_id") or payload.get("session_id") or "")}
    tool = payload.get("tool_name")
    path = edited_path(payload)
    if event == "beforeShellExecution":
        return dict(base, tool_name="Bash", tool_input={"command": str(payload.get("command", ""))},
                    hook_event_name="PreToolUse")
    if event == "preToolUse":
        if tool != "Write":
            return None
        if not path:
            note("preToolUse Write payload names no file path (tried %s); allowing" % ", ".join(PATH_KEYS))
            return None
        return dict(base, tool_name="Write", tool_input={"file_path": path}, hook_event_name="PreToolUse")
    if event == "afterFileEdit":
        return dict(base, tool_name="Edit", tool_input={"file_path": path}, hook_event_name="PostToolUse")
    if event == "postToolUse":
        match = SKILL_FILE.search(path) if tool == "Read" else None
        if match:  # an agent loads a skill by reading its SKILL.md
            return dict(base, tool_name="Skill", tool_input={"skill": match.group(1)}, hook_event_name="PostToolUse")
        if tool == "Write":
            return dict(base, tool_name="Write", tool_input={"file_path": path}, hook_event_name="PostToolUse")
        return None
    if event in ("sessionStart", "preCompact"):
        source = "startup" if event == "sessionStart" else "compact"
        return dict(base, hook_event_name="SessionStart", source=source)
    if event == "beforeSubmitPrompt":
        return dict(base, hook_event_name="UserPromptSubmit", prompt=str(payload.get("prompt", "")))
    loops = payload.get("loop_count")
    return dict(base, hook_event_name="Stop", stop_hook_active=isinstance(loops, int) and loops > 0)


def run_script(script, args, claude, project):
    command = [sys.executable, script] if script.endswith(".py") else [script]
    if not os.path.isfile(script):
        raise Failure("hook script %s does not exist" % script)
    env = dict(os.environ)
    env.setdefault("CLAUDE_PROJECT_DIR", project)
    try:
        done = subprocess.run(command + args, input=json.dumps(claude), capture_output=True, text=True,
                              env=env, cwd=project if os.path.isdir(project) else None, timeout=TIMEOUT_SECONDS)
    except subprocess.TimeoutExpired:
        raise Failure("hook script %s timed out after %ss" % (script, TIMEOUT_SECONDS))
    except OSError as err:
        raise Failure("hook script %s cannot run: %s" % (script, err))
    return done.returncode, done.stdout, done.stderr.strip()


def interpret(stdout):
    """(blocked reason or None, context text) from a hook script's stdout."""
    text = stdout.strip()
    if not text:
        return None, ""
    try:
        data = json.loads(text)
    except ValueError:
        return None, text
    if not isinstance(data, dict):
        return None, ""
    specific = data.get("hookSpecificOutput")
    specific = specific if isinstance(specific, dict) else {}
    context = specific.get("additionalContext")
    context = context if isinstance(context, str) else ""
    if data.get("decision") == "block":
        return str(data.get("reason") or "blocked by a fleet-board hook"), context
    if specific.get("permissionDecision") == "deny":
        return str(specific.get("permissionDecisionReason") or "denied by a fleet-board hook"), context
    return None, context


def result(event, blocked, context):
    """The JSON object Cursor expects for this event."""
    if event in GUARDS:
        if blocked is not None:
            return {"permission": "deny", "user_message": blocked, "agent_message": blocked}
        return {"permission": "allow"}
    if event == "stop":
        return {"followup_message": blocked} if blocked is not None else {}
    if event == "beforeSubmitPrompt":
        message = blocked if blocked is not None else context
        out = {"continue": blocked is None}
        if message:
            out["user_message"] = message
        return out
    if event == "preCompact":
        return {"user_message": context} if context else {}
    return {"additional_context": context} if context else {}


def emit(value):
    print(json.dumps(value))


def main(argv):
    if len(argv) < 3 or argv[1] not in EVENTS:
        note("usage: cursor-hook.py <%s> <hook-script> [args...]" % "|".join(sorted(EVENTS)))
        return 2
    event, script, args = argv[1], argv[2], argv[3:]
    guard = event in GUARDS
    try:
        payload = json.load(sys.stdin)
        if not isinstance(payload, dict):
            raise ValueError("payload is not a JSON object")
    except ValueError as err:
        note("unreadable %s payload: %s" % (event, err))
        emit(result(event, "cursor-hook: unreadable %s payload (fail closed)" % event, "") if guard
             else result(event, None, ""))
        return 0
    project = project_dir(payload)
    claude = claude_payload(event, payload, str(payload.get("cwd") or project))
    if claude is None:
        emit(result(event, None, ""))
        return 0
    if not os.path.isabs(script):
        script = os.path.join(project, script)
    try:
        code, stdout, stderr = run_script(script, args, claude, project)
        if code not in (0, 2):
            raise Failure("hook script %s exited %d: %s" % (script, code, stderr or "no message"))
    except Failure as err:
        note(str(err))
        emit(result(event, "cursor-hook: %s (fail closed)" % err, "") if guard else result(event, None, ""))
        return 0
    if code == 2:
        message = stderr or "blocked by a fleet-board hook (exit 2, no message)"
        if guard:
            emit(result(event, message, ""))
            return 0
        print(message, file=sys.stderr)
        return 2
    if stderr:
        print(stderr, file=sys.stderr)
    blocked, context = interpret(stdout)
    emit(result(event, blocked, context))
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
