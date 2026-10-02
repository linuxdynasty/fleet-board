#!/usr/bin/env python3
"""Require named fleet-board roles for Codex child spawns (PreToolUse hook).

Codex's spawn tool is `collaborationspawn_agent` (observed on 0.158.0; repeat
the probe before trusting the name on a newer Codex). A generic or unknown
agent_type is denied with the allowed role names, so a child never silently
runs on the default model. The allowlist is the role catalog in
.agents/roles.json. Explicit per-spawn model fields pass through untouched: the
coordinator honours the goal's model choices and verifies the child model.

Spawns from a workspace outside this checkout, and every other tool, pass.
"""
from __future__ import annotations

import json
import sys
from pathlib import Path
from typing import Any, Optional

ROOT = Path(__file__).resolve().parents[1]
CANONICAL_TOOL = "collaborationspawn_agent"


def deny(reason: str) -> dict:
    """Codex's supported PreToolUse denial envelope."""
    return {"hookSpecificOutput": {
        "hookEventName": "PreToolUse",
        "permissionDecision": "deny",
        "permissionDecisionReason": reason,
    }}


def guard(payload: Any) -> Optional[dict]:
    """Check a spawn payload, leaving unrelated calls and workspaces alone."""
    if not isinstance(payload, dict):
        return deny("fleet-board spawn guard received malformed hook input; retry with a named fleet role.")
    if payload.get("tool_name") != CANONICAL_TOOL:
        return None
    cwd = payload.get("cwd")
    if not isinstance(cwd, str) or not cwd:
        return deny("fleet-board spawn guard needs the session cwd to validate this spawn.")
    if not Path(cwd).resolve().is_relative_to(ROOT):
        return None
    try:
        roles = json.loads((ROOT / ".agents/roles.json").read_text())["roles"]
        if not isinstance(roles, dict) or not roles:
            raise ValueError("empty role catalog")
    except (OSError, ValueError, TypeError, KeyError):
        return deny("fleet-board role manifest is unavailable or invalid; restore .agents/roles.json before spawning.")
    tool_input = payload.get("tool_input")
    if not isinstance(tool_input, dict):
        return deny("fleet-board spawn input is malformed; specify agent_type with a named fleet role.")
    role = tool_input.get("agent_type")
    if not isinstance(role, str) or role not in roles:
        return deny("Select a named fleet-board agent_type for this spawn: %s. Generic or unknown roles are not allowed."
                    % ", ".join(sorted(roles)))
    return None


def main() -> None:
    """Read one hook event and emit a denial only when needed."""
    try:
        payload = json.load(sys.stdin)
    except (ValueError, TypeError):
        payload = None
    decision = guard(payload)
    if decision is not None:
        print(json.dumps(decision))


if __name__ == "__main__":
    main()
