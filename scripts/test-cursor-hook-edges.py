#!/usr/bin/env python3
"""Failure-mode and boundary cases for scripts/cursor-hook.py.

Timeouts, the child's working directory, non-UTF-8 and huge output, deeply
nested JSON, the stop status filter, which Read paths count as a skill load, and
the Cursor hint added to the skill-nudge refusal. Fixtures: cursor_hook_testkit.py.
"""
from __future__ import annotations

import json
import os
import subprocess
import sys
import time
import unittest
from pathlib import Path

from cursor_hook_testkit import CONVO, Base, out_json

NESTED = "[" * 200000 + "]" * 200000


class StopStatusTests(Base):
    def stop(self, status):
        stub = self.p.recording_stub()
        payload = {"loop_count": 0} if status is None else {"status": status, "loop_count": 0}
        return out_json(self, self.p.adapter("stop", stub, payload)), (self.p.tmp / "seen.json").exists()

    def test_completed_runs_the_script(self):
        self.assertEqual(self.stop("completed"), ({}, True))

    def test_aborted_error_and_missing_status_do_not_run_it(self):
        for status in ("aborted", "error", "other", None):
            self.assertEqual(self.stop(status), ({}, False), status)
            (self.p.tmp / "seen.json").unlink(missing_ok=True)

    def test_an_interrupted_turn_is_not_refused_through_hooks_json(self):
        self.p.goal("Finish the migration")
        for status in ("aborted", "error"):
            self.assertEqual(out_json(self, self.p.fire_one("stop", {"status": status, "loop_count": 0})), {}, status)
        done = self.p.fire_one("stop", {"status": "completed", "loop_count": 0})
        self.assertIn("followup_message", out_json(self, done))


class TimeoutTests(Base):
    def test_a_slow_guard_is_denied_naming_the_timeout(self):
        slow = self.p.stub("slow.sh", "sleep 5\n")
        started = time.time()
        result = self.p.adapter("beforeShellExecution", slow, {"command": "ls"}, FLEET_CURSOR_HOOK_TIMEOUT="1")
        self.assertLess(time.time() - started, 4)
        message = self.deny_message(out_json(self, result))
        self.assertIn("timed out after 1s", message)

    def test_a_slow_advisory_hook_fails_open(self):
        slow = self.p.stub("slow.sh", "sleep 5\n")
        result = self.p.adapter("postToolUse", slow, {"tool_name": "Write", "tool_input": {"file_path": "a"}},
                                FLEET_CURSOR_HOOK_TIMEOUT="1")
        self.assertEqual(out_json(self, result), {})
        self.assertIn("timed out after 1s", result.stderr)

    def test_the_default_timeout_is_eight_seconds(self):
        slow = self.p.stub("slow.sh", "sleep 12\n")
        started = time.time()
        result = self.p.adapter("beforeShellExecution", slow, {"command": "ls"})
        self.assertLess(time.time() - started, 11)
        self.assertIn("timed out after 8s", self.deny_message(out_json(self, result)))

    def test_a_bad_override_falls_back_to_the_default(self):
        ok = self.p.stub("ok.sh", "exit 0\n")
        for value in ("abc", "0", "-3", ""):
            result = self.p.adapter("beforeShellExecution", ok, {"command": "ls"}, FLEET_CURSOR_HOOK_TIMEOUT=value)
            self.assertEqual(out_json(self, result), {"permission": "allow"}, value)


class ChildEnvironmentTests(Base):
    def test_the_script_runs_in_the_project_root(self):
        stub = self.p.stub("pwd.sh", 'cat >/dev/null; pwd -P > "%s/pwd.txt"\n' % self.p.tmp)
        payload = json.dumps({"status": "completed", "loop_count": 0, "cwd": "/"})
        subprocess.run([sys.executable, str(self.p.root / "scripts/cursor-hook.py"), "stop", str(stub)], input=payload,
                       text=True, capture_output=True, env=self.p.env(), cwd="/")  # started somewhere else
        self.assertEqual((self.p.tmp / "pwd.txt").read_text().strip(), os.path.realpath(self.p.root))


class BadOutputTests(Base):
    garbage_out = "cat >/dev/null; printf '\\377\\376 not utf8'\n"
    garbage_err = "cat >/dev/null; printf 'bad \\377 bytes' >&2; exit 2\n"

    def test_non_utf8_stdout_from_a_guard_does_not_crash_it(self):
        stub = self.p.stub("g.sh", self.garbage_out)
        self.assertEqual(out_json(self, self.p.adapter("beforeShellExecution", stub, {"command": "ls"})),
                         {"permission": "allow"})

    def test_non_utf8_stderr_on_a_guard_refusal_keeps_the_message(self):
        stub = self.p.stub("g.sh", self.garbage_err)
        message = self.deny_message(out_json(self, self.p.adapter("beforeShellExecution", stub, {"command": "ls"})))
        self.assertIn("bad", message)
        self.assertIn("bytes", message)

    def test_non_utf8_output_from_an_advisory_hook_does_not_crash_it(self):
        stub = self.p.stub("a.sh", self.garbage_out)
        value = out_json(self, self.p.adapter("postToolUse", stub, {"tool_name": "Write", "tool_input": {}}))
        self.assertIn("additional_context", value)
        advisory_refusal = self.p.stub("r.sh", self.garbage_err)
        result = self.p.adapter("stop", advisory_refusal, {"status": "completed"})
        self.assertEqual(result.returncode, 2)

    def test_deeply_nested_payload_json_fails_closed_for_a_guard_and_open_otherwise(self):
        stub = self.p.stub("ok.sh", "exit 0\n")
        guard = self.p.adapter("preToolUse", stub, NESTED)
        self.assertEqual(out_json(self, guard)["permission"], "deny")
        self.assertEqual(out_json(self, self.p.adapter("postToolUse", stub, NESTED)), {})
        self.assertEqual(out_json(self, self.p.adapter("stop", stub, NESTED)), {})

    def test_deeply_nested_json_from_the_script_does_not_crash_the_adapter(self):
        stub = self.p.stub("deep.sh", "cat >/dev/null; python3 -c \"print('['*200000 + ']'*200000)\"\n")
        self.assertEqual(out_json(self, self.p.adapter("beforeShellExecution", stub, {"command": "ls"}))["permission"],
                         "allow")

    def test_huge_messages_are_cut_down(self):
        refuse = self.p.stub("big.sh", "cat >/dev/null; head -c 300000 /dev/zero | tr '\\0' x >&2; exit 2\n")
        message = self.deny_message(out_json(self, self.p.adapter("beforeShellExecution", refuse, {"command": "ls"})))
        self.assertLess(len(message), 30000)
        self.assertIn("truncated", message)
        context = self.p.stub("ctx.sh", "cat >/dev/null; head -c 300000 /dev/zero | tr '\\0' y\n")
        value = out_json(self, self.p.adapter("sessionStart", context, {}))
        self.assertLess(len(value["additional_context"]), 30000)


class SkillReadTests(Base):
    def read(self, path):
        payload = {"tool_name": "Read", "tool_input": {"file_path": path}, "cwd": str(self.p.root)}
        out_json(self, self.p.fire_one("postToolUse", payload, "Read"))

    def nudge(self):
        payload = {"tool_name": "Write", "tool_input": {"file_path": str(self.p.root / "api/fleet/x.proto")}}
        return out_json(self, self.p.fire_one("preToolUse", payload, "Write"))["permission"]

    def test_only_project_skill_directories_count(self):
        name = "fleet-contract-rigor"
        outside = self.p.tmp / "elsewhere" / "skills" / name
        outside.mkdir(parents=True)
        (outside / "SKILL.md").write_text("x")
        for path in ("/etc/skills/%s/SKILL.md" % name, str(self.p.root / "docs/skills" / name / "SKILL.md"),
                     str(outside / "SKILL.md"), str(self.p.root / ".agents/skills/x/../../../../elsewhere/skills" / name / "SKILL.md")):
            self.read(path)
            self.assertEqual(self.nudge(), "deny", path)

    def test_a_skill_directory_symlinked_outside_the_project_does_not_count(self):
        outside = self.p.tmp / "elsewhere" / "fleet-contract-rigor"
        outside.mkdir(parents=True)
        (outside / "SKILL.md").write_text("x")
        (self.p.root / ".cursor/skills").mkdir()
        (self.p.root / ".cursor/skills/fleet-contract-rigor").symlink_to(outside)
        self.read(str(self.p.root / ".cursor/skills/fleet-contract-rigor/SKILL.md"))
        self.assertEqual(self.nudge(), "deny")

    def test_each_project_skill_directory_counts_and_a_relative_path_uses_cwd(self):
        for base in (".agents/skills", ".claude/skills", ".cursor/skills"):
            for k in Path(self.p.state).glob("fleet-skill-loaded-*"):
                k.unlink()
            self.read("%s/fleet-contract-rigor/SKILL.md" % base if base == ".claude/skills"
                      else str(self.p.root / base / "fleet-contract-rigor/SKILL.md"))
            self.assertEqual(self.nudge(), "allow", base)


class CursorHintTests(Base):
    def test_the_agent_message_says_how_to_load_a_skill_in_cursor(self):
        payload = {"tool_name": "Write", "tool_input": {"file_path": str(self.p.root / "web/src/App.tsx")}}
        value = out_json(self, self.p.fire_one("preToolUse", payload, "Write"))
        self.assertIn(".agents/skills/fleet-frontend/SKILL.md", value["agent_message"])
        self.assertNotIn("In Cursor", value["user_message"])
        self.assertIn("BLOCKED (skill-nudge)", value["user_message"])

    def test_other_refusals_get_no_hint(self):
        refuse = self.p.stub("refuse.sh", "echo 'BLOCKED: other' >&2; exit 2\n")
        value = out_json(self, self.p.adapter("beforeShellExecution", refuse, {"command": "ls"}))
        self.assertEqual(value["agent_message"], value["user_message"])


if __name__ == "__main__":
    unittest.main(verbosity=1)
