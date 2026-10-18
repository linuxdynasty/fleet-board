#!/usr/bin/env python3
"""Exercise skill-named goal routing at each harness prompt boundary."""

import json
import os
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
ROUTER = ROOT / "scripts/goal-role-router.py"


def run(args, payload, cwd=ROOT):
    return subprocess.run([sys.executable, str(ROUTER), *args], input=json.dumps(payload),
                          capture_output=True, text=True, cwd=cwd)


def context(prompt, harness, cwd=None):
    result = run([harness], {"prompt": prompt, "cwd": str(cwd or ROOT), "hook_event_name": "UserPromptSubmit"})
    assert result.returncode == 0, result.stderr
    return json.loads(result.stdout)["hookSpecificOutput"]["additionalContext"] if result.stdout else ""


class GoalRoleRouterTests(unittest.TestCase):
    def test_claude_goal_file_routes_phases_and_keeps_issue_driver(self):
        with tempfile.TemporaryDirectory() as tmp:
            goal = Path(tmp) / "goal.txt"
            goal.write_text("GOAL: Drive issue #12 with fleet-issue-driver. Diagnose with fleet-diagnostic-rigor, "
                            "implement a fix using fleet-go, then use fleet-code-review.")
            output = context("/goal %s" % goal, "claude", tmp)
        for marker in ("debug-fleet", "develop-fleet", "review-fleet", "sonnet", "fleet-issue-driver stays"):
            self.assertIn(marker, output)
        self.assertIn("select its named .claude/agents agent type", output)
        self.assertIn("omit a model override", output)

    def test_muse_goal_text_routes_performance_and_review(self):
        output = context("/goal Drive epic with fleet-issue-driver. Benchmark using fleet-performance-rigor; "
                         "review the final PR using fleet-code-review.", "muse")
        self.assertIn("perf-fleet", output)
        self.assertIn("review-fleet", output)
        self.assertIn("muse-exec", output)
        self.assertIn("- develop-fleet:", output)

    def test_drive_routes_implementation_and_independent_review(self):
        for harness in ("claude", "codex", "muse"):
            for prompt in ("/goal drive epics #12, #13 using fleet-issue-driver",
                           "/goal drive issues #12, #13", "/goal drive\n#12"):
                with self.subTest(harness=harness, prompt=prompt):
                    output = context(prompt, harness)
                    self.assertIn("- develop-fleet:", output)
                    self.assertIn("- review-fleet:", output)
                    self.assertIn("DRIVE CONTRACT", output)
                    self.assertIn("The owner merges", output)
                    self.assertIn("authorized endpoint", output)
                    self.assertIn("fleet-issue-driver stays", output)
                    self.assertIn("watcher", output)

    def test_driver_invocations_allow_the_article(self):
        for harness in ("claude", "codex", "muse"):
            for verb in ("Use", "Apply", "Follow"):
                with self.subTest(harness=harness, verb=verb):
                    output = context("/goal %s the fleet-issue-driver to drive issue #12" % verb, harness)
                    self.assertIn("DRIVE CONTRACT", output)
                    self.assertIn("- develop-fleet:", output)
                    self.assertIn("- review-fleet:", output)

    def test_drive_detection_does_not_promote_diagnostic_work(self):
        for prompt in ("/goal update the fleet-issue-driver skill docs",
                       "/goal Diagnose why fleet-issue-driver hook fires using fleet-diagnostic-rigor",
                       "/goal Diagnose drive latency; the issue is flaky using fleet-diagnostic-rigor",
                       "/goal drive the latency investigation; issue #12 using fleet-diagnostic-rigor",
                       "/goal drive epics #12suffix", "/goal ship it"):
            with self.subTest(prompt=prompt):
                output = context(prompt, "codex")
                self.assertNotIn("DRIVE CONTRACT", output)
                self.assertNotIn("- develop-fleet:", output)

    def test_drive_keeps_deep_bindings_and_deduplicates_roles(self):
        output = context("/goal Drive issue #12 with fleet-issue-driver; implement a difficult fix "
                         "and review using fleet-code-review", "codex")
        self.assertEqual(1, output.count("- develop-fleet-deep:"))
        self.assertEqual(1, output.count("- review-fleet-deep:"))
        self.assertNotIn("- develop-fleet:", output)

    def test_muse_instructions_require_skills_and_manifest_bindings(self):
        output = context("/goal Diagnose using fleet-diagnostic-rigor", "muse")
        self.assertIn("load each named skill body", output)
        self.assertIn("model and effort", output)
        self.assertNotIn("DRIVE CONTRACT", output)
        self.assertNotIn("Muse dispatch", context("/goal Diagnose using fleet-diagnostic-rigor", "claude"))

    def test_codex_debug_model_is_manifest_binding(self):
        workhorse = json.loads((ROOT / ".agents/roles.json").read_text())["models"]["codex"]["workhorse"]
        output = context("GOAL: Diagnose the sync with fleet-diagnostic-rigor", "codex")
        self.assertIn("debug-fleet", output)
        self.assertIn("%s effort=%s" % (workhorse["model"], workhorse["effort"]), output)
        self.assertIn("codex-exec", output)

    def test_deep_diagnosis_uses_deep_tier(self):
        output = context("/goal Diagnose an intermittent cross-system failure using fleet-diagnostic-rigor", "codex")
        self.assertIn("debug-fleet-deep", output)
        self.assertIn("gpt-6-sol effort=high", output)

    def test_arbiter_wording_routes_arbiter_tier(self):
        base = "/goal Drive issue with fleet-issue-driver; "
        for tail in ("take each design call to an arbiter", "send the design fork for arbitration",
                     "get a ruling before merge"):
            with self.subTest(tail=tail):
                self.assertIn("- arbiter-fleet:", context(base + tail, "claude"))
        self.assertIn("fable effort=high", context(base + "get a ruling", "claude"))
        self.assertIn("gpt-6-astra effort=high", context(base + "get a ruling", "codex"))

    def test_deep_versus_workhorse_implementation_and_review(self):
        deep = context("/goal Implement a difficult lease fix using fleet-go, then review it using fleet-code-review", "codex")
        self.assertIn("- develop-fleet-deep:", deep)
        self.assertIn("- review-fleet-deep:", deep)
        plain = context("/goal Implement a fix using fleet-go, then review it using fleet-code-review", "codex")
        self.assertIn("- develop-fleet:", plain)
        self.assertIn("- review-fleet:", plain)
        self.assertNotIn("-deep:", plain)
        self.assertNotIn("arbiter-fleet", plain)

    def test_skill_phrasing_and_case(self):
        for prompt in ("/goal Apply fleet-go", "/goal Tighten the contract using fleet-contract-rigor"):
            with self.subTest(prompt=prompt):
                self.assertIn("develop-fleet", context(prompt, "codex"))
        self.assertIn("debug-fleet", context("/goal Diagnose with Fleet-Diagnostic-Rigor", "codex"))
        writeup = context("/goal Write up findings with fleet-diagnostic-rigor", "codex")
        self.assertIn("debug-fleet", writeup)
        self.assertNotIn("develop-fleet", writeup)

    def test_long_inline_goal_is_not_mistaken_for_a_path(self):
        prompt = "/goal " + "Drive issue with fleet-issue-driver. " * 20 + "Review using fleet-code-review."
        self.assertIn("review-fleet", context(prompt, "claude"))

    def test_non_goal_control_and_skill_free_prompts_are_quiet(self):
        for prompt in ("please use fleet-code-review", "/goal done", "/goal consent push", "/goal clear",
                       "/goal ship it"):
            with self.subTest(prompt=prompt):
                self.assertEqual("", context(prompt, "codex"))

    def test_empty_catalog_and_malformed_manifest_are_quiet(self):
        for manifest in (None, "{malformed"):
            with tempfile.TemporaryDirectory() as tmp:
                work = Path(tmp)
                (work / "scripts").mkdir()
                (work / ".agents/skills").mkdir(parents=True)
                if manifest is None:
                    shutil.copyfile(ROOT / ".agents/roles.json", work / ".agents/roles.json")
                else:
                    (work / ".agents/roles.json").write_text(manifest)
                    (work / ".agents/skills/fleet-go").mkdir()
                    (work / ".agents/skills/fleet-go/SKILL.md").write_text("x")
                copy = work / "scripts/goal-role-router.py"
                shutil.copyfile(ROUTER, copy)
                result = subprocess.run([sys.executable, str(copy), "codex"],
                                        input=json.dumps({"prompt": "/goal Diagnose with fleet-go"}),
                                        capture_output=True, text=True, cwd=work)
                self.assertEqual(0, result.returncode)
                self.assertEqual("", result.stdout)

    def test_prepared_goal_rejects_outside_and_secret_paths(self):
        with tempfile.TemporaryDirectory() as workspace, tempfile.TemporaryDirectory() as elsewhere:
            outside = Path(elsewhere) / "goal.txt"
            outside.write_text("Diagnose with fleet-diagnostic-rigor")
            secret = Path(workspace) / ".env"
            secret.write_text("Diagnose with fleet-diagnostic-rigor")
            self.assertEqual("", context("/goal %s" % outside, "codex", workspace))
            self.assertEqual("", context("/goal %s" % secret, "codex", workspace))

    def test_project_hook_commands_inject_context(self):
        payload = json.dumps({"session_id": "wire-probe", "cwd": str(ROOT), "model": "x",
                              "prompt": "/goal drive issues #12, #13",
                              "hook_event_name": "UserPromptSubmit"})
        configs = {"codex": ".codex/hooks.json", "claude": ".claude/settings.json", "muse": ".muse/hooks.json"}
        for harness, config in configs.items():
            with self.subTest(harness=harness):
                hooks = json.loads((ROOT / config).read_text())["hooks"]["UserPromptSubmit"][0]["hooks"]
                command = next(h["command"] for h in hooks if "goal-role-router.py" in h["command"])
                env = {**os.environ, "CLAUDE_PROJECT_DIR": str(ROOT), "CODEX_PROJECT_DIR": str(ROOT)}
                result = subprocess.run(["/bin/sh", "-c", command], input=payload, capture_output=True,
                                        text=True, cwd=ROOT, env=env, check=True)
                output = json.loads(result.stdout)["hookSpecificOutput"]["additionalContext"]
                self.assertIn("- develop-fleet:", output)
                self.assertIn("- review-fleet:", output)
                self.assertIn("DRIVE CONTRACT", output)

    def test_prompt_wrappers_fail_quiet_outside_repo(self):
        with tempfile.TemporaryDirectory() as elsewhere:
            for config in (".codex/hooks.json", ".claude/settings.json", ".muse/hooks.json"):
                with self.subTest(config=config):
                    hooks = json.loads((ROOT / config).read_text())["hooks"]["UserPromptSubmit"][0]["hooks"]
                    command = next(h["command"] for h in hooks if "goal-role-router.py" in h["command"])
                    env = {k: v for k, v in os.environ.items() if k not in {"CODEX_PROJECT_DIR", "CLAUDE_PROJECT_DIR"}}
                    result = subprocess.run(["/bin/sh", "-c", command],
                                            input=json.dumps({"prompt": "/goal Diagnose with fleet-diagnostic-rigor"}),
                                            capture_output=True, text=True, cwd=elsewhere, env=env)
                    self.assertEqual(0, result.returncode)
                    self.assertEqual("", result.stdout)

    def test_prepared_goal_survives_refresh(self):
        with tempfile.TemporaryDirectory() as tmp:
            work = Path(tmp)
            (work / "goal.txt").write_text("Drive issue with fleet-issue-driver.\nDiagnose using "
                                           "fleet-diagnostic-rigor.\nReview with fleet-code-review.\n")
            hook = ROOT / ".claude/hooks/goal-refresh.sh"
            env = {**os.environ, "FLEET_HOOK_STATE_DIR": tmp, "HOME": tmp}

            def submit(prompt):
                payload = json.dumps({"session_id": "router-refresh", "cwd": str(work), "prompt": prompt,
                                      "hook_event_name": "UserPromptSubmit"})
                return subprocess.run(["bash", str(hook)], input=payload, capture_output=True, text=True,
                                      cwd=ROOT, env=env, check=True).stdout

            submit("/goal goal.txt")
            stored = (work / ".claude/active-goal.router-refresh").read_text()
            self.assertIn("fleet-diagnostic-rigor", stored)
            self.assertIn("fleet-code-review", stored)
            refreshed = json.loads(submit("continue"))["hookSpecificOutput"]["additionalContext"]
            self.assertIn("fleet-diagnostic-rigor", refreshed)

    def dispatch(self, tool_input, tmp, session="session-1"):
        result = run(["claude-dispatch"], {"tool_name": "Agent", "tool_input": tool_input, "cwd": tmp,
                                           "session_id": session, "hook_event_name": "PreToolUse"})
        self.assertEqual(0, result.returncode)
        return json.loads(result.stdout)["hookSpecificOutput"] if result.stdout else None

    def test_claude_dispatch_restores_role_bindings(self):
        with tempfile.TemporaryDirectory() as tmp:
            goal = Path(tmp) / ".claude" / "active-goal.session-1"
            goal.parent.mkdir()
            goal.write_text("SESSION: session-1\nDrive the issue with fleet-issue-driver\n")
            for role, override, expected in (("arbiter-fleet", "opus", "fable"),
                                             ("review-fleet-deep", "sonnet", "opus"),
                                             ("debug-fleet-deep", "haiku", "opus")):
                with self.subTest(role=role):
                    tool_input = {"subagent_type": role, "model": override, "prompt": "x", "description": "x"}
                    output = self.dispatch(tool_input, tmp)
                    self.assertEqual("PreToolUse", output["hookEventName"])
                    self.assertNotIn("model", output["updatedInput"])
                    self.assertIn("selects " + expected, output["additionalContext"])

    def test_claude_dispatch_leaves_goal_named_models_and_other_cases_alone(self):
        with tempfile.TemporaryDirectory() as tmp:
            goal = Path(tmp) / ".claude" / "active-goal.session-1"
            goal.parent.mkdir()
            tool_input = {"subagent_type": "debug-fleet-deep", "model": "haiku", "prompt": "x", "description": "x"}
            goal.write_text("SESSION: session-1\nUse Haiku for debug-fleet-deep\n")
            self.assertIsNone(self.dispatch(tool_input, tmp))
            goal.write_text("CONSENT: push\nSESSION: session-1\nDiagnose\n")
            self.assertIsNotNone(self.dispatch(tool_input, tmp))
            goal.write_text("SESSION: other-session\nDiagnose\n")
            self.assertIsNone(self.dispatch(tool_input, tmp))
            goal.write_text("SESSION: session-1\nDONE\n")
            self.assertIsNone(self.dispatch(tool_input, tmp))
            goal.write_text("SESSION: session-1\nDiagnose\n")
            self.assertIsNone(self.dispatch({**tool_input, "subagent_type": "Explore"}, tmp))
            self.assertIsNone(self.dispatch({**tool_input, "subagent_type": "debug-fleet", "model": "sonnet"}, tmp))
            goal.unlink()
            self.assertIsNone(self.dispatch(tool_input, tmp))

    def test_claude_dispatch_hook_is_wired(self):
        config = json.loads((ROOT / ".claude/settings.json").read_text())
        commands = [h["command"] for row in config["hooks"]["PreToolUse"] if row.get("matcher") == "Agent"
                    for h in row["hooks"]]
        self.assertEqual(1, len(commands))
        with tempfile.TemporaryDirectory() as tmp:
            goal = Path(tmp) / ".claude" / "active-goal.session-1"
            goal.parent.mkdir()
            goal.write_text("SESSION: session-1\nDiagnose with fleet-diagnostic-rigor\n")
            payload = json.dumps({"tool_name": "Agent", "tool_input": {
                "subagent_type": "debug-fleet-deep", "model": "haiku", "prompt": "x", "description": "x"},
                "cwd": tmp, "session_id": "session-1", "hook_event_name": "PreToolUse"})
            result = subprocess.run(["/bin/sh", "-c", commands[0]], input=payload, capture_output=True,
                                    text=True, cwd=ROOT, env={**os.environ, "CLAUDE_PROJECT_DIR": str(ROOT)},
                                    check=True)
            self.assertNotIn("model", json.loads(result.stdout)["hookSpecificOutput"]["updatedInput"])


if __name__ == "__main__":
    unittest.main()
