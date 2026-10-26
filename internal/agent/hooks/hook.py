#!/usr/bin/env python3
"""Fleet goal hook (G1).

Session mapping (E9), turn-end events (E8) and the Stop guard for one
harness (argv[1]: claude, codex, muse or cursor). The hook exits 0 with
no output unless the payload matches the agent's goal registry, so the
developer's own sessions see no change. Every parse failure fails open:
an unreadable registry, goal file or payload allows the turn.

The payload arrives on stdin from the harness. `--input-file <path>`
reads it from a file instead, for the agent's own verify runs (which
must start the script through the one-shot helper, without stdin) and
for debugging.


Registry: $FLEET_GOAL_REGISTRY, else $FLEET_AGENT_DATA_DIR/goal-registry.json,
else ~/.local/share/fleet-agent/goal-registry.json, as
{"version": 1, "entries": [{"goal_id", "harness", "pane", "worktree_path",
"harness_session_id", "repo_driver", "repo_ships_own}]}. Matched mapping
events with a new session id write it back (atomic temp file plus rename)
and spool one JSON line to $FLEET_HOOK_SPOOL (or the data-dir default
hook-events.jsonl).
"""

import errno
import json
import os
import sys
import time

HOOK_VERSION = "1"

MAPPING_EVENTS = {"UserPromptSubmit", "PreToolUse", "beforeSubmitPrompt"}
TURN_END_EVENTS = {"Stop", "stop"}
INTERRUPT_EVENTS = {"Interrupt"}
INERT_EVENTS = {"SessionStart", "sessionStart", "sessionEnd"}

PANE_HARNESSES = {"claude", "codex", "cursor"}
CWD_HARNESSES = {"muse", "cursor"}


def fail_open():
    sys.exit(0)


def data_dir():
    override = os.environ.get("FLEET_AGENT_DATA_DIR", "")
    if override:
        return override
    home = os.environ.get("HOME", "")
    if not home:
        return ""
    return os.path.join(home, ".local", "share", "fleet-agent")


def registry_path():
    override = os.environ.get("FLEET_GOAL_REGISTRY", "")
    if override:
        return override
    base = data_dir()
    if not base:
        return ""
    return os.path.join(base, "goal-registry.json")


def spool_path():
    override = os.environ.get("FLEET_HOOK_SPOOL", "")
    if override:
        return override
    base = data_dir()
    if not base:
        return ""
    return os.path.join(base, "hook-events.jsonl")


def load_registry(path):
    try:
        with open(path, "r", encoding="utf-8") as handle:
            data = json.load(handle)
    except (OSError, ValueError):
        return None
    if not isinstance(data, dict):
        return None
    entries = data.get("entries", [])
    if not isinstance(entries, list):
        return None
    data["entries"] = [e for e in entries if isinstance(e, dict)]
    return data


def save_registry(path, data):
    try:
        directory = os.path.dirname(path)
        if directory:
            os.makedirs(directory, exist_ok=True)
        tmp = path + ".fleet-tmp-%d" % os.getpid()
        fd = os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
        with os.fdopen(fd, "w", encoding="utf-8") as handle:
            json.dump(data, handle)
        os.replace(tmp, path)
    except OSError:
        pass


def spool(line):
    path = spool_path()
    if not path:
        return
    try:
        directory = os.path.dirname(path)
        if directory:
            os.makedirs(directory, exist_ok=True)
        # Plain append, created 0600 like every file the agent makes
        # (01d2): open() would follow the process umask instead.
        fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_APPEND, 0o600)
        with os.fdopen(fd, "a", encoding="utf-8") as handle:
            handle.write(json.dumps(line) + "\n")
    except OSError:
        pass


def under(path, root):
    return root == path or root.startswith(path.rstrip("/") + "/")


def match(payload, harness, entries):
    sid = payload.get("session_id") or payload.get("conversation_id") or ""
    if sid:
        for entry in entries:
            if entry.get("harness") == harness and entry.get("harness_session_id") == sid:
                return entry
    pane = os.environ.get("TMUX_PANE", "")
    if pane and harness in PANE_HARNESSES:
        for entry in entries:
            if entry.get("harness") == harness and entry.get("pane") == pane:
                return entry
    roots = []
    if isinstance(payload.get("workspace_roots"), list):
        roots = [r for r in payload["workspace_roots"] if isinstance(r, str)]
    elif isinstance(payload.get("cwd"), str):
        roots = [payload["cwd"]]
    if roots and harness in CWD_HARNESSES:
        cands = []
        for entry in entries:
            if entry.get("harness") != harness:
                continue
            if entry.get("harness_session_id"):
                continue
            if entry.get("repo_driver"):
                continue
            work = entry.get("worktree_path", "")
            if not isinstance(work, str) or not work:
                continue
            if any(under(work, root) for root in roots):
                cands.append(entry)
        # Only when exactly one pane is registered for that checkout:
        # the hook never guesses.
        if len(cands) == 1:
            return cands[0]
    return None


def clean_text(text, limit):
    out = []
    for char in str(text):
        code = ord(char)
        if code < 0x20 or code == 0x7F or (0x80 <= code <= 0x9F):
            continue
        out.append(char)
        if len(out) >= limit:
            break
    return "".join(out)


def pid_alive(pid_text):
    try:
        pid = int(pid_text, 10)
    except (TypeError, ValueError):
        return False
    if pid <= 0:
        return False
    try:
        os.kill(pid, 0)
    except OSError as exc:
        # No permission still means the process exists.
        return exc.errno == errno.EPERM
    return True


def guard_allows(worktree, goal_id):
    """The Stop guard: allow only a finished or externally-waited goal."""
    if not isinstance(worktree, str) or not worktree:
        return True, ""
    if not isinstance(goal_id, str) or not goal_id:
        return True, ""
    if "/" in goal_id or goal_id in (".", ".."):
        return True, ""
    try:
        with open(os.path.join(worktree, ".fleet-board", "goal-" + goal_id),
                  "r", encoding="utf-8") as handle:
            first = handle.readline(4097).rstrip("\n")
    except OSError:
        # No goal file says nothing unfinished: fail open.
        return True, ""
    if first == "DONE":
        return True, ""
    if first.startswith("BLOCKED"):
        watch = ""
        owner = ""
        action = ""
        for token in first.split():
            if token.startswith("WATCH="):
                watch = token[len("WATCH="):]
            elif token.startswith("OWNER="):
                owner = token[len("OWNER="):]
            elif token.startswith("ACTION="):
                action = token[len("ACTION="):]
        if watch:
            if pid_alive(watch):
                return True, ""
        elif owner and action:
            return True, ""
        reason = "fleet goal %s is blocked: %s" % (goal_id, clean_text(first, 120))
        return False, reason
    reason = "fleet goal %s is not done: %s" % (goal_id, clean_text(first, 120))
    return False, reason


def block_answer(harness, reason):
    if harness == "cursor":
        return {"followup_message": reason}
    return {"decision": "block", "reason": reason}


def now_utc():
    return time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())


def main():
    harness = sys.argv[1] if len(sys.argv) > 1 else ""
    input_file = ""
    rest = sys.argv[2:]
    if "--input-file" in rest:
        at = rest.index("--input-file")
        if at + 1 < len(rest):
            input_file = rest[at + 1]
    if input_file:
        try:
            with open(input_file, "r", encoding="utf-8") as handle:
                raw = handle.read()
        except OSError:
            fail_open()
    else:
        try:
            raw = sys.stdin.read()
        except OSError:
            fail_open()
    if not raw or not raw.strip():
        fail_open()
    try:
        payload = json.loads(raw)
    except ValueError:
        fail_open()
    if not isinstance(payload, dict):
        fail_open()
    # The documented hook-test shape wraps the payload.
    if isinstance(payload.get("stdin"), dict) and isinstance(payload.get("event"), str):
        payload = payload["stdin"]
    event = payload.get("hook_event_name", "")
    if not isinstance(event, str):
        fail_open()
    if event in INERT_EVENTS:
        fail_open()
    if event not in MAPPING_EVENTS and event not in TURN_END_EVENTS and event not in INTERRUPT_EVENTS:
        fail_open()
    path = registry_path()
    if not path:
        fail_open()
    registry = load_registry(path)
    if registry is None:
        fail_open()
    entry = match(payload, harness, registry["entries"])
    if entry is None:
        fail_open()
    goal_id = entry.get("goal_id", "")
    sid = payload.get("session_id") or payload.get("conversation_id") or ""
    if event in MAPPING_EVENTS:
        if sid and entry.get("harness_session_id") != sid:
            entry["harness_session_id"] = sid
            save_registry(path, registry)
            spool({"v": 1, "time": now_utc(), "harness": harness,
                   "event": event, "kind": "session_mapping",
                   "goal_id": goal_id, "harness_session_id": sid,
                   "pane": os.environ.get("TMUX_PANE", "")})
        fail_open()
    if event in INTERRUPT_EVENTS:
        spool({"v": 1, "time": now_utc(), "harness": harness,
               "event": event, "kind": "interrupt",
               "goal_id": goal_id, "harness_session_id": sid,
               "pane": os.environ.get("TMUX_PANE", "")})
        fail_open()
    # Turn end. A repo-owned checkout stays inert: two Stop guards must
    # never fight.
    if entry.get("repo_ships_own"):
        fail_open()
    status = payload.get("status", "")
    if isinstance(status, str) and status and status != "completed":
        # An aborted or errored turn is never held: only a finished turn
        # can be refused.
        allowed, reason = True, ""
    else:
        allowed, reason = guard_allows(entry.get("worktree_path", ""), goal_id)
    line = {"v": 1, "time": now_utc(), "harness": harness,
            "event": event, "kind": "turn_end", "allowed": allowed,
            "goal_id": goal_id, "harness_session_id": sid,
            "pane": os.environ.get("TMUX_PANE", "")}
    if isinstance(status, str) and status:
        line["status"] = status
    spool(line)
    if allowed:
        fail_open()
    if harness == "codex":
        # No probe shows Codex accepting a Stop block; the hook stays
        # notify-only rather than guessing an output shape.
        fail_open()
    sys.stdout.write(json.dumps(block_answer(harness, reason)))
    sys.exit(0)


main()
