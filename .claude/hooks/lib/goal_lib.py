#!/usr/bin/env python3
"""Goal-file helpers shared by goal_stop.py, goal_refresh.py and on-compact.sh.

A goal file is plain text:

    SESSION: <session id>          header; binds the goal to one session
    CONSENT: push, pr-open         zero or more grants the owner already gave
    <first line of the goal>       DONE | BLOCKED: ... | anything (working)
    <rest of the goal>

SESSION and CONSENT lines count as metadata only while they are a LEADING
block (the first ordinary line ends it, and only the first SESSION is the
header), so a goal that merely discusses the format is never truncated or read
as a grant. Both hooks parse through this one module so they cannot decide a
file differently.

Files: .claude/active-goal.<session id> (written by goal-refresh), or the
owner's hand-written .claude/active-goal (read, never written by a session),
or $CLAUDE_GOAL_FILE. All are gitignored.

CLI (used by on-compact.sh):
  goal_lib.py describe SESSION_ID CWD   print "<file>\\n<consent>\\n<goal text>"
                                        for this session's active goal, or nothing
"""
from __future__ import annotations

import os
import re
import sys
from pathlib import Path

MARKER = ".agents/skill-paths.json"
CONSENT_RE = re.compile(r"^\s*consent:(.*)$", re.IGNORECASE)
DONE_RE = re.compile(r"^(?:DONE|done)(?![A-Za-z0-9_])")
BLOCKED_RE = re.compile(r"^(?:BLOCKED|blocked):(.*)$")
MAX_READ = 262144


def safe(value):
    return re.sub(r"[^A-Za-z0-9._-]", "-", value)


def project_root(cwd):
    """The fleet-board checkout containing cwd, or None."""
    if not cwd:
        return None
    d = Path(cwd)
    for candidate in (d, *d.parents):
        if (candidate / MARKER).is_file():
            return candidate
    return None


def goal_dirs(cwd):
    """Directories whose .claude/ may hold this session's goal, root first."""
    dirs = []
    root = project_root(cwd)
    for d in (root, Path(cwd) if cwd else None):
        if d is not None and d not in dirs:
            dirs.append(d)
    return dirs


def write_target(session_id, cwd):
    """Where /goal writes: $CLAUDE_GOAL_FILE, else the root's per-session file."""
    explicit = os.environ.get("CLAUDE_GOAL_FILE")
    if explicit:
        return Path(explicit)
    dirs = goal_dirs(cwd)
    if not dirs:
        return None
    return dirs[0] / ".claude" / ("active-goal." + safe(session_id))


def parse(text):
    """Returns (header session id or None, consent acts, body text)."""
    lines = text.splitlines()
    header, consent, i = None, [], 0
    while i < len(lines):
        stripped = lines[i].strip()
        match = CONSENT_RE.match(lines[i])
        if not stripped:
            pass
        elif match:
            if match.group(1).strip():
                consent.append(match.group(1).strip())
        elif stripped.startswith("SESSION:") and header is None:
            header = stripped[len("SESSION:"):].strip()
        else:
            break
        i += 1
    return header, consent, "\n".join(lines[i:]).strip()


def read_text(path):
    try:
        if Path(path).stat().st_size > MAX_READ:
            return ""
        return Path(path).read_text(errors="replace")
    except OSError:
        return ""


def retired(path):
    """A DONE tombstone, which must not shadow an active goal further down."""
    first = parse(read_text(path))[2].split("\n", 1)[0]
    return bool(DONE_RE.match(first))


def find_file(session_id, cwd):
    """First existing, non-retired candidate; an explicit override is final."""
    explicit = os.environ.get("CLAUDE_GOAL_FILE")
    if explicit and Path(explicit).is_file():
        return Path(explicit)
    names = [".claude/active-goal." + safe(session_id), ".claude/active-goal"]
    candidates = [d / n for n in names for d in goal_dirs(cwd)]
    if os.environ.get("HOME"):
        candidates.append(Path(os.environ["HOME"]) / ".claude" / "active-goal")
    for path in candidates:
        if path.is_file() and not retired(path):
            return path
    return None


class Goal:
    def __init__(self, path, header, consent, body):
        self.path, self.header, self.consent, self.body = path, header, consent, body

    @property
    def first_line(self):
        return self.body.split("\n", 1)[0]

    @property
    def done(self):
        return bool(DONE_RE.match(self.first_line))

    @property
    def blocked(self):
        """The BLOCKED reason text, "" for a bare BLOCKED:, None if not blocked."""
        match = BLOCKED_RE.match(self.first_line)
        return match.group(1).strip() if match else None


def active_goal(session_id, cwd):
    """This session's goal, or None (no file, empty, or another session's)."""
    path = find_file(session_id, cwd)
    if path is None:
        return None
    header, consent, body = parse(read_text(path))
    if header is not None and header != session_id:
        return None
    if not body:
        return None
    return Goal(path, header, consent, body)


def env_consent():
    value = os.environ.get("CLAUDE_GOAL_CONSENT", "").strip()
    return [value] if value else []


def clip(text, path, limit=4000):
    if len(text) <= limit:
        return text
    return text[:limit].rstrip() + "\n... (clipped; the full goal is in " + str(path) + ")"


def state_dir():
    return (os.environ.get("FLEET_HOOK_STATE_DIR") or os.environ.get("TMPDIR") or "/tmp").rstrip("/") or "/"


def sid12(session_id):
    return safe(session_id)[:12] or "na"


def skill_marker(session_id, skill):
    """The marker skill-loaded.sh writes when a skill is loaded in this session."""
    return Path(state_dir()) / ("fleet-skill-loaded-%s-%s" % (sid12(session_id), skill))


def missing_named_skills(text, root, session_id):
    """Project skills the goal text names that this session has not loaded."""
    if root is None:
        return []
    skills_dir = Path(root) / ".agents" / "skills"
    try:
        ids = sorted(p.name for p in skills_dir.iterdir() if p.is_dir())
    except OSError:
        return []
    named = (i for i in ids if re.search(
        r"(?<![A-Za-z0-9_-])" + re.escape(i) + r"(?![A-Za-z0-9_-])", text))
    return [i for i in named if not skill_marker(session_id, i).exists()]


def write_atomic(path, text):
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.with_name(path.name + ".tmp")
    tmp.write_text(text)
    os.replace(tmp, path)


def main(argv):
    if len(argv) == 4 and argv[1] == "describe":
        goal = active_goal(argv[2], argv[3])
        if goal is not None and not goal.done:
            print(goal.path)
            print(", ".join(goal.consent + env_consent()))
            print(clip(goal.body, goal.path, 2000))
        return 0
    return 2


if __name__ == "__main__":
    try:
        sys.exit(main(sys.argv))
    except Exception:  # hooks fail open
        sys.exit(0)
