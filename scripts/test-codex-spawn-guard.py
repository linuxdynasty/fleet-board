#!/usr/bin/env python3
"""Test the Codex spawn guard through its configured PreToolUse command."""

import json
import os
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
GUARD = ROOT / "scripts/guard-codex-spawn.py"
CANONICAL_TOOL = "collaborationspawn_agent"


def hook_environment(project_dir):
    """A hook environment without Git's parent-hook repository bindings."""
    env = {k: v for k, v in os.environ.items() if not k.startswith("GIT_")}
    env["CODEX_PROJECT_DIR"] = project_dir
    return env


class CodexSpawnGuardTests(unittest.TestCase):
    """Exercise the hook wiring and the payload observed on Codex 0.158."""

    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.workspace = Path(self.temporary.name).resolve()
        subprocess.run(["git", "init", "-q", str(self.workspace)],
                       env=hook_environment(str(self.workspace)), check=True)
        (self.workspace / "scripts").mkdir()
        (self.workspace / ".agents").mkdir()
        shutil.copyfile(GUARD, self.workspace / "scripts/guard-codex-spawn.py")
        shutil.copyfile(ROOT / ".agents/roles.json", self.workspace / ".agents/roles.json")
        hooks = json.loads((ROOT / ".codex/hooks.json").read_text())["hooks"]
        matching = [row for row in hooks["PreToolUse"] if row.get("matcher") == "^collaborationspawn_agent$"]
        self.assertEqual(1, len(matching))
        self.command = matching[0]["hooks"][0]["command"]

    def run_command(self, payload, cwd=None, env=None, raw=None):
        return subprocess.run(
            ["/bin/sh", "-c", self.command],
            input=raw if raw is not None else json.dumps(payload),
            capture_output=True, text=True, cwd=cwd or self.workspace,
            env=env or hook_environment(str(self.workspace)),
        )

    def invoke(self, tool_input, tool_name=CANONICAL_TOOL, cwd=None):
        result = self.run_command({
            "hook_event_name": "PreToolUse", "tool_name": tool_name, "tool_input": tool_input,
            "cwd": cwd or str(self.workspace), "model": "gpt-6-luna", "session_id": "probe-session",
        })
        self.assertEqual(0, result.returncode)
        return json.loads(result.stdout)["hookSpecificOutput"] if result.stdout else None

    def test_known_role_passes_without_rewrite(self):
        self.assertIsNone(self.invoke({"agent_type": "debug-fleet-deep", "message": "Diagnose"}))

    def test_generic_unknown_and_missing_roles_are_denied(self):
        for role in ("default", "worker", "mystery", None):
            with self.subTest(role=role):
                decision = self.invoke({"agent_type": role, "message": "Work"})
                self.assertEqual("deny", decision["permissionDecision"])
                self.assertIn("scan-fleet", decision["permissionDecisionReason"])
                self.assertIn("develop-fleet", decision["permissionDecisionReason"])

    def test_explicit_model_and_effort_overrides_pass_unchanged(self):
        for override in ({"model": "gpt-6-luna"}, {"reasoning_effort": "low"}):
            with self.subTest(override=override):
                self.assertIsNone(self.invoke({"agent_type": "debug-fleet", "message": "x", **override}))

    def test_sibling_project_environment_does_not_select_other_worktree(self):
        with tempfile.TemporaryDirectory() as sibling:
            result = self.run_command(
                {"hook_event_name": "PreToolUse", "tool_name": CANONICAL_TOOL,
                 "tool_input": {"agent_type": "default", "message": "Work"}, "cwd": str(self.workspace)},
                env=hook_environment(sibling))
            decision = json.loads(result.stdout)["hookSpecificOutput"]
            self.assertEqual("deny", decision["permissionDecision"])

    def test_missing_git_root_blocks_despite_project_environment(self):
        with tempfile.TemporaryDirectory() as elsewhere:
            result = self.run_command(
                {"hook_event_name": "PreToolUse", "tool_name": CANONICAL_TOOL,
                 "tool_input": {"agent_type": "default"}, "cwd": elsewhere},
                cwd=elsewhere, env=hook_environment(str(ROOT)))
            self.assertEqual(2, result.returncode)
            self.assertIn("worktree", result.stderr)

    def test_malformed_input_and_missing_manifest_fail_closed(self):
        self.assertEqual("deny", self.invoke(["not", "an", "object"])["permissionDecision"])
        malformed = self.run_command(None, raw="{not-json")
        self.assertEqual("deny", json.loads(malformed.stdout)["hookSpecificOutput"]["permissionDecision"])
        (self.workspace / ".agents/roles.json").unlink()
        self.assertEqual("deny", self.invoke({"agent_type": "scan-fleet", "message": "Scan"})["permissionDecision"])

    def test_non_spawn_and_other_workspace_are_quiet(self):
        self.assertIsNone(self.invoke({"command": "true"}, tool_name="Bash"))
        with tempfile.TemporaryDirectory() as elsewhere:
            self.assertIsNone(self.invoke({"agent_type": "default"}, cwd=elsewhere))

    def test_every_manifest_role_is_allowed(self):
        roles = json.loads((ROOT / ".agents/roles.json").read_text())["roles"]
        for name in roles:
            with self.subTest(role=name):
                self.assertIsNone(self.invoke({"agent_type": name, "message": "x"}))


if __name__ == "__main__":
    unittest.main()
