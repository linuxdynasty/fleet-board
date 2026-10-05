"""Shared fixtures for test-cursor-hook.py and test-cursor-hook-edges.py.

Seeded RED/GREEN cases for scripts/cursor-hook.py and .cursor/hooks.json.

Everything goes through public surfaces: the adapter's command line with
Cursor-shaped JSON on stdin, and the exact command strings in .cursor/hooks.json
run through `sh -c` from a seeded project root, the way Cursor runs them. The
real hook scripts under .claude/hooks do the work; a few stub scripts stand in
where no wired hook exercises an event or an exit code.

Write tool_input is {file_path, content} and Read is {file_path} (cursor-agent
2026.10.01); the docs do not say so, so the adapter also tries other names (see
test_write_path_field_names) and, when none is present, allows the edit and says
so on stderr (test_write_without_a_path).
"""
from __future__ import annotations

import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
ADAPTER = ROOT / "scripts/cursor-hook.py"
CONVO = "convo-1234567890"

# Event -> matcher tool names expected to be wired in .cursor/hooks.json.
WIRED = {
    "preToolUse": ["Write"],
    "postToolUse": ["Write", "Read"],
    "preCompact": [None],
    "stop": [None],
    "beforeSubmitPrompt": [None],
    "subagentStart": [None],
}


class Project:
    """A throwaway fleet-board-like project plus an isolated hook environment."""

    def __init__(self):
        self.tmp = Path(tempfile.mkdtemp())
        self.root = self.tmp / "proj"
        self.state = self.tmp / "state"
        self.home = self.tmp / "home"
        for d in (self.root, self.state, self.home):
            d.mkdir(parents=True)
        (self.root / ".agents").mkdir()
        shutil.copy(ROOT / ".agents/skill-paths.json", self.root / ".agents/")
        shutil.copytree(ROOT / ".claude/hooks", self.root / ".claude/hooks", symlinks=True)
        (self.root / "scripts").mkdir()
        shutil.copy(ROOT / "scripts/check-docs-stale.py", self.root / "scripts/")
        if ADAPTER.exists():
            shutil.copy(ADAPTER, self.root / "scripts/")
        if (ROOT / "scripts/cursor_hook_family.py").exists():
            shutil.copy(ROOT / "scripts/cursor_hook_family.py", self.root / "scripts/")
        (self.root / ".cursor").mkdir()
        if (ROOT / ".cursor/hooks.json").exists():
            shutil.copy(ROOT / ".cursor/hooks.json", self.root / ".cursor/")
        subprocess.run(["git", "init", "-q", str(self.root)], check=True)

    def env(self, **extra):
        env = {k: v for k, v in os.environ.items() if not k.startswith(("CLAUDE_", "CURSOR_", "FLEET_"))}
        env.update(HOME=str(self.home), FLEET_HOOK_STATE_DIR=str(self.state), CURSOR_PROJECT_DIR=str(self.root))
        # Tests snapshot every file under tmp; Python's bytecode cache lands there (scripts/__pycache__ on Linux,
        # $HOME/Library/Caches for Apple's python3) and is not the adapter's write.
        env["PYTHONDONTWRITEBYTECODE"] = "1"
        env.update(extra)
        return env

    def adapter(self, event, script, payload, **env):
        """Runs `python3 cursor-hook.py <event> <script>` once."""
        data = payload if isinstance(payload, str) else json.dumps(payload)
        return subprocess.run([sys.executable, str(self.root / "scripts/cursor-hook.py"), event, str(script)],
                              input=data, capture_output=True, text=True, env=self.env(**env), cwd=self.root)

    def commands(self, event, tool=None):
        config = json.loads((self.root / ".cursor/hooks.json").read_text())
        rows = config["hooks"].get(event, [])
        return [r for r in rows if tool is None or "matcher" not in r or re.search(r["matcher"], tool)]

    def fire(self, event, payload, tool=None):
        """Runs every hooks.json command Cursor would run for this event."""
        data = json.dumps(dict(payload, hook_event_name=event, conversation_id=CONVO,
                               workspace_roots=[str(self.root)]))
        return [subprocess.run(["sh", "-c", row["command"]], input=data, capture_output=True, text=True,
                               env=self.env(), cwd=self.root, timeout=30) for row in self.commands(event, tool)]

    def fire_as(self, event, payload, tool=None):
        """Like fire_one, but the payload's own ids stand: no conversation_id is added."""
        data = json.dumps(dict(payload, hook_event_name=event, workspace_roots=[str(self.root)]))
        rows = self.commands(event, tool)
        assert len(rows) == 1, "%s/%s wired %d times" % (event, tool, len(rows))
        return subprocess.run(["sh", "-c", rows[0]["command"]], input=data, capture_output=True, text=True,
                              env=self.env(), cwd=self.root, timeout=30)

    def fire_one(self, event, payload, tool=None):
        results = self.fire(event, payload, tool)
        assert len(results) == 1, "%s/%s wired %d times" % (event, tool, len(results))
        return results[0]

    def stub(self, name, body):
        path = self.tmp / name
        path.write_text("#!/bin/sh\n" + body)
        path.chmod(0o755)
        return path

    def recording_stub(self):
        """A hook script that records stdin and CLAUDE_PROJECT_DIR, then exits 0 silently."""
        return self.stub("record.sh", '/bin/cat > "%s/seen.json"; printf %%s "$CLAUDE_PROJECT_DIR" > "%s/seen.env"\n'
                         % (self.tmp, self.tmp))

    def seen(self):
        return json.loads((self.tmp / "seen.json").read_text()), (self.tmp / "seen.env").read_text()

    def goal(self, text, sid=CONVO):
        (self.root / ".claude").mkdir(exist_ok=True)
        (self.root / ".claude" / ("active-goal." + sid)).write_text("SESSION: %s\n%s\n" % (sid, text))

    def cleanup(self):
        shutil.rmtree(self.tmp, ignore_errors=True)


def out_json(case, result):
    """Cursor treats invalid JSON on a permission hook as a block: always demand one object."""
    case.assertEqual(result.returncode, 0, result.stderr)
    value = json.loads(result.stdout)
    case.assertIsInstance(value, dict)
    case.assertEqual(result.stdout.strip().count("\n"), 0, "one JSON object on one line")
    return value


class Base(unittest.TestCase):
    def setUp(self):
        self.p = Project()
        self.addCleanup(self.p.cleanup)

    def write_payload(self, path, key="file_path", **extra):
        return dict({"tool_name": "Write", "tool_input": {key: path}, "cwd": str(self.p.root)}, **extra)

    def deny_message(self, value):
        self.assertEqual(value["permission"], "deny")
        self.assertTrue(value["agent_message"].startswith(value["user_message"]))
        return value["agent_message"]
