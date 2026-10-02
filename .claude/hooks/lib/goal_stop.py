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

stop_hook_active=true means this stop already follows one refusal, so it is
allowed: that is the loop breaker, in Claude Code and in Muse (both send it).
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
    if payload.get("stop_hook_active") in (True, "true", "True"):
        return
    goal = goal_lib.active_goal(sid, str(payload.get("cwd") or ""))
    if goal is None or goal.done:
        return
    head, why_allowed = refusal_head(goal)
    if why_allowed:
        print("goal-continue: stop allowed, " + why_allowed, file=sys.stderr)
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
