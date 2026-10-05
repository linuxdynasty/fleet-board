"""Helper-agent links and the opt-in payload log for scripts/cursor-hook.py.

A Cursor helper agent (Task tool) may run its tools under its own ids. For the
skill events (preToolUse, postToolUse) a parent and its helpers must share one
marker key, as a Claude Code subagent shares its parent's session_id. subagentStart
records the links in the hook state dir; session_key follows them to the root.

State files, all in <state dir> (FLEET_HOOK_STATE_DIR, else TMPDIR, else /tmp, as
.claude/hooks/lib/common.sh) and never cleaned up except by the OS temp reaper;
losing one only makes skill-nudge block again until the skill is read again:
  fleet-cursor-link-<child id>   holds the parent id
  fleet-cursor-root-<parent id>  marks a conversation that started helpers

No network, standard library only. Ids from a payload are data: each one is
checked (valid_id) before it becomes part of a file name.
"""
from __future__ import annotations

import json
import os
import re
import stat
import sys

# Helper agents: one small file per link, <state dir>/fleet-cursor-link-<child id> holding the parent id.
STATE_ENV = "FLEET_HOOK_STATE_DIR"  # the same variable and default as .claude/hooks/lib/common.sh
LINK_PREFIX = "fleet-cursor-link-"
# <state dir>/fleet-cursor-root-<id> marks a conversation that has started helpers, for a helper whose
# own ids are all unknown but whose session_id is the parent's.
ROOT_PREFIX = "fleet-cursor-root-"
ID_PATTERN = re.compile(r"[A-Za-z0-9][A-Za-z0-9._-]{0,127}")
MAX_LINK_DEPTH = 8
SESSION_FIELDS = ("conversation_id", "session_id", "subagent_id")
# Opt-in payload log (a file path): one JSON line per event, ids only, for the live check of the links.
LOG_ENV = "FLEET_CURSOR_HOOK_LOG"
LOG_IDS = ("conversation_id", "session_id", "subagent_id", "parent_conversation_id")
LOG_VALUE_LIMIT = 128
LOG_KEY_LIMIT = 64


def note(message):
    print("cursor-hook: " + message, file=sys.stderr)


def state_dir():
    d = os.environ.get(STATE_ENV) or os.environ.get("TMPDIR") or "/tmp"
    return d[:-1] if d.endswith("/") else d


def valid_id(value):
    """An id that is safe as a file name: bounded, no slash, no `..`."""
    return isinstance(value, str) and ID_PATTERN.fullmatch(value) is not None and ".." not in value


def read_link(child):
    """The parent id recorded for `child`, or ""."""
    flags = os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0)
    try:
        fd = os.open("%s/%s%s" % (state_dir(), LINK_PREFIX, child), flags)
        with os.fdopen(fd, "rb") as handle:
            parent = handle.read(256).decode("utf-8", "replace").strip()
    except OSError:
        return ""
    return parent if valid_id(parent) else ""


def link_chain(sid):
    """The ids from `sid` up to its root, and whether a cycle or the depth bound cut the walk short."""
    path = []
    cur = sid
    while valid_id(cur):
        if cur in path:
            return path, path[path.index(cur):]
        path.append(cur)
        if len(path) > MAX_LINK_DEPTH:
            break
        cur = read_link(cur)
    return path, None


def root_session(sid):
    path, cycle = link_chain(sid)
    if cycle:
        return min(cycle)  # the same answer whichever member of the loop the walk entered at
    return path[-1] if path else sid


def chat_key(payload):
    """The per-chat key the non-skill events use: Claude fires Stop, UserPromptSubmit and
    SessionStart for the main conversation only, never for a subagent."""
    return str(payload.get("conversation_id") or payload.get("session_id") or "")


def session_key(payload):
    """The id skill markers are keyed by: the root of the family of the payload's own ids.

    A helper agent runs its tools under its own ids; the links written by subagentStart lead
    them to the parent, as a Claude Code subagent keeps the parent's session_id. With no link
    the key is the conversation id, as before.
    """
    for field in SESSION_FIELDS:
        value = payload.get(field)
        if valid_id(value):
            root = root_session(value)
            if root != value:
                return root
    for field in SESSION_FIELDS:  # a helper whose own ids are unknown but whose session_id is the parent's
        value = payload.get(field)
        if valid_id(value) and os.path.exists("%s/%s%s" % (state_dir(), ROOT_PREFIX, value)):
            return value
    return chat_key(payload)


def regular_file(path):
    """False only for something that exists and is not a regular file (a symlink, a directory)."""
    try:
        return stat.S_ISREG(os.lstat(path).st_mode)
    except OSError:
        return True  # gone again: nothing to report


def write_once(name, content):
    """Writes <state dir>/<name> atomically unless it exists. Never raises.

    The scratch file is created exclusively and without following a symlink, so a
    planted link at its name cannot redirect the write; a destination that exists
    but is not a regular file is reported, not trusted.
    """
    directory = state_dir()
    scratch = "%s/.%s.%d.tmp" % (directory, name, os.getpid())
    final = "%s/%s" % (directory, name)
    flags = os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, "O_NOFOLLOW", 0)
    made = False
    try:
        with os.fdopen(os.open(scratch, flags, 0o600), "w") as handle:
            made = True
            handle.write(content)
        os.link(scratch, final)
    except FileExistsError:
        if made and not regular_file(final):
            note("%s exists and is not a regular file; left alone" % final)
        elif not made:
            note("cannot record %s: scratch file %s already exists" % (name, scratch))
    except OSError as err:
        note("cannot record %s: %s" % (name, err))
    finally:
        if made:
            try:
                os.unlink(scratch)
            except OSError:
                pass


def write_link(child, parent):
    """Records child -> parent once. A link that would close a loop is not written."""
    if child == parent or child in link_chain(parent)[0] or read_link(child):
        return
    write_once(LINK_PREFIX + child, parent + "\n")


def record_links(payload):
    """subagentStart: remember which conversation a helper belongs to (by subagent_id and conversation_id)."""
    parent = payload.get("parent_conversation_id")
    if not valid_id(parent):
        note("subagentStart payload has no usable parent_conversation_id; no link recorded")
        return
    for child in (payload.get("subagent_id"), payload.get("conversation_id")):
        if valid_id(child):
            write_once(ROOT_PREFIX + parent, "\n")
            write_link(child, parent)


def log_value(value):
    return value[:LOG_VALUE_LIMIT] if isinstance(value, str) else None


def log_event(event, payload, key):
    """Opt-in (FLEET_CURSOR_HOOK_LOG): one JSON line with the event, key names and ids, nothing else."""
    path = os.environ.get(LOG_ENV)
    if not path:
        return
    row = {"event": event, "keys": sorted(str(k)[:LOG_KEY_LIMIT] for k in payload)[:LOG_KEY_LIMIT]}
    row.update({name: log_value(payload.get(name)) for name in LOG_IDS})
    row["resolved_key"] = log_value(key)
    try:
        fd = os.open(path, os.O_WRONLY | os.O_APPEND | os.O_CREAT, 0o600)
        with os.fdopen(fd, "w") as handle:
            handle.write(json.dumps(row) + "\n")
    except OSError as err:
        note("cannot write hook log %s: %s" % (path, err))
