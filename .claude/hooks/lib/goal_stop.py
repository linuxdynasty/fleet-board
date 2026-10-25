#!/usr/bin/env python3
"""Stop hook logic: refuse to end the turn while this session's goal is open.

stdin : {session_id, cwd, stop_hook_active, hook_event_name: "Stop", ...}
stdout: {"decision": "block", "reason": "..."} to refuse the stop, else nothing
exit  : always 0. A hook that can refuse to end a session must never be the
        reason a session breaks, so every parse failure allows the stop.

The first line of the goal file decides (the agent writes it, so each escape
is something the hook can check or the owner can see on stderr):

  no file, empty, another session's   allow
  DONE                                allow
  BLOCKED: .. WATCH=<pid>             allow while the pid is alive; a dead
                                      watcher REFUSES (nothing would wake the agent)
  BLOCKED: .. OWNER=<who> ACTION=<what>
                                      allow: a named person has to act, and the
                                      collector turns it into one Triage item
  BLOCKED: <reason>  (no watcher/owner)
                                      REFUSE: escalate to arbiter-fleet instead
  anything else                       REFUSE: keep working

A no-progress budget bounds the loop instead of stopping after one refusal:
each block is counted per session (reset by the next prompt, which clears it
via goal-refresh, or by any allowed stop); the stop is allowed once the count
passes MAX_BLOCKS. DONE naming PR/review work without stable-read evidence is
also refused, so a closed goal cannot hide an open review.
CONSENT: lines (and $CLAUDE_GOAL_CONSENT) are echoed back so the agent does not
stop to ask again for something the owner already granted; nothing verifies
them, so every honoured grant is also echoed to stderr.
"""
from __future__ import annotations

import json
import os
import re
import sys

import goal_lib

REASON_TAIL = """Continue it now. Take the next concrete action yourself rather than describing what could be done. Do not ask the owner and do not stop on a question: if the code, a local doc or a cheap experiment cannot settle it, escalate to arbiter-fleet with the raw observations (not your hypothesis) and act on its RULING.

Stop for real only when one of these is true, and say which:
  - the goal is met: put DONE on the first line of {path}
  - you are waiting on outside work behind a live watcher: put BLOCKED: <reason> WATCH=<pid> on the first line
  - a named person must act: put BLOCKED: <reason> OWNER=<who> ACTION=<what> on the first line"""


MAX_BLOCKS = 3

PR_SIGNALS = re.compile(r"(?i)(pull request|\bprs?\b|review (thread|comment)s?|\bCI\b|\bchecks\b|merge conflict)")
PR_EVIDENCE = re.compile(r"(?i)(stable read|green twice|two stable|no open|no unresolved|\bmerged\b|\bclosed\b|green on)")


def sid12(sid):
    return re.sub(r"[^A-Za-z0-9._-]", "-", sid or "")[:12] or "na"


def state_dir():
    return os.environ.get("FLEET_HOOK_STATE_DIR") or os.environ.get("TMPDIR") or "/tmp"


def block_count(sid):
    path = os.path.join(state_dir(), "fleet-stop-blocks-" + sid12(sid))
    try:
        with open(path) as fh:
            return max(0, int(fh.read().strip() or 0))
    except (OSError, ValueError):
        return 0


def reset_blocks(sid):
    try:
        os.unlink(os.path.join(state_dir(), "fleet-stop-blocks-" + sid12(sid)))
    except OSError:
        pass


def note_block(sid):
    count = block_count(sid) + 1
    try:
        with open(os.path.join(state_dir(), "fleet-stop-blocks-" + sid12(sid)), "w") as fh:
            fh.write(str(count))
    except OSError:
        pass
    return count


def pr_done_refusal(body):
    if PR_SIGNALS.search(body or "") and not PR_EVIDENCE.search(body or ""):
        return ("Your goal closes with DONE but names PR/review work without stable-read evidence. "
                "Verify the PR head, checks and review threads first "
                "(`gh pr view`, `gh pr checks`, thread list), address what is open, "
                "or leave BLOCKED with a live WATCH pid instead of DONE.")
    return None


def retired_pr_refusal(sid, cwd):
    """A retired (DONE) goal of this session naming PR work without evidence.

    DONE files are tombstones that active_goal ignores, so a premature DONE
    would otherwise release the stop with an open review. Returns (path,
    refusal, body) or None. Fail open: any read problem means no refusal.
    """
    try:
        from pathlib import Path
        names = [".claude/active-goal." + goal_lib.safe(sid)]
        dirs = goal_lib.goal_dirs(cwd)
        explicit = os.environ.get("CLAUDE_GOAL_FILE")
        paths = [d / n for n in names for d in dirs if d is not None]
        if explicit:
            paths.insert(0, Path(explicit))
        for path in paths:
            try:
                if not path.is_file():
                    continue
                text = goal_lib.read_text(path)
                header, _, body = goal_lib.parse(text)
                if header is not None and header != sid:
                    continue
                first = (body.split("\n", 1)[0] if body else "")
                if not goal_lib.DONE_RE.match(first):
                    continue
                refusal = pr_done_refusal(body)
                if refusal is not None:
                    return str(path), refusal, body
            except OSError:
                continue
    except Exception:
        pass
    return None


def pid_alive(pid):
    if pid < 2:  # 0 is the process group and 1 never dies: not a real watcher
        return False
    try:
        os.kill(pid, 0)
    except ProcessLookupError:
        return False
    except PermissionError:
        return True
    return True


def refusal_head(goal):
    """Returns (head text, None) to refuse, or (None, why) to allow the stop."""
    reason = goal.blocked
    if not reason:
        return "You are stopping with an active goal still open. Do not end the turn on a status report.", None
    watch = re.search(r"WATCH=(\d+)", reason)
    owner = re.search(r"OWNER=(\S+)", reason)
    action = re.search(r"ACTION=(.+)$", reason)
    if watch:
        pid = int(watch.group(1))
        if pid_alive(pid):
            return None, "BLOCKED behind live watcher pid %d: %s" % (pid, reason)
        return ("Your goal file claims BLOCKED with WATCH=%d, but that watcher process is NOT running. "
                "Nothing will wake you, so waiting is not an option: re-arm the watcher or do the work now." % pid), None
    if owner and action and action.group(1).strip():
        return None, "BLOCKED for %s to act: %s" % (owner.group(1), action.group(1).strip())
    return ("Your goal file claims BLOCKED (%s) but names no watcher and no OWNER/ACTION, so nothing will wake "
            "you and nobody is coming. Blocked is not a reason to stop: escalate the question to arbiter-fleet "
            "now, or name a watcher with WATCH=<pid>, or a person with OWNER=<who> ACTION=<what>." % reason), None


def main():
    payload = json.load(sys.stdin)
    if not isinstance(payload, dict) or os.environ.get("CLAUDE_GOAL_OFF") == "1":
        return
    sid = payload.get("session_id")
    if not isinstance(sid, str) or not sid:
        return
    cwd = str(payload.get("cwd") or "")
    goal = goal_lib.active_goal(sid, cwd)
    if goal is None:
        retired = retired_pr_refusal(sid, cwd)
        if retired is None:
            reset_blocks(sid)
            return
        path, refusal, body = retired
        count = note_block(sid)
        if count > MAX_BLOCKS:
            reset_blocks(sid)
            print("goal-continue: no-progress budget spent (%d), stop allowed" % MAX_BLOCKS, file=sys.stderr)
            return
        print(json.dumps({"decision": "block", "reason": refusal + "\n\nRETIRED GOAL (%s):\n%s\n\n%s" % (
            path, goal_lib.clip(body, path), REASON_TAIL.format(path=path))}))
        return
    if goal.done:
        reset_blocks(sid)
        return
    head, why_allowed = refusal_head(goal)
    if why_allowed:
        reset_blocks(sid)
        print("goal-continue: stop allowed, " + why_allowed, file=sys.stderr)
        return
    count = note_block(sid)
    if count > MAX_BLOCKS:
        reset_blocks(sid)
        print("goal-continue: no-progress budget spent (%d), stop allowed" % MAX_BLOCKS, file=sys.stderr)
        return
    consent = goal.consent + goal_lib.env_consent()
    note = ""
    if consent:
        granted = ", ".join(consent)
        print("goal-continue: consent honoured, not asking again for: " + granted, file=sys.stderr)
        note = ("\n\nOWNER CONSENT ALREADY GRANTED for: %s\nDo those yourself now. Stopping to ask again for "
                "something on that list is not a valid stop: the owner already answered." % granted)
    reason = "%s\n\nACTIVE GOAL (%s):\n%s%s\n\n%s" % (
        head, goal.path, goal_lib.clip(goal.body, goal.path), note, REASON_TAIL.format(path=goal.path))
    print(json.dumps({"decision": "block", "reason": reason}))


if __name__ == "__main__":
    try:
        main()
    except Exception:  # fail open
        pass
    sys.exit(0)
