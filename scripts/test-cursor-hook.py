#!/usr/bin/env python3
"""Seeded RED/GREEN cases for scripts/cursor-hook.py and .cursor/hooks.json.

Fixtures and the shape of every case are in cursor_hook_testkit.py; failure
modes (timeouts, bad output, deep JSON) are in test-cursor-hook-edges.py.
"""
from __future__ import annotations

import json
import subprocess
import sys
import unittest

from cursor_hook_testkit import ADAPTER, CONVO, ROOT, WIRED, Base, Project, out_json


class WiringTests(Base):
    def test_hooks_json_wires_exactly_the_documented_events(self):
        config = json.loads((self.p.root / ".cursor/hooks.json").read_text())
        self.assertEqual(config["version"], 1)
        got = {event: [row.get("matcher") for row in rows] for event, rows in config["hooks"].items()}
        self.assertEqual(got, WIRED)

    def test_the_guard_fails_closed_and_nothing_else_does(self):
        config = json.loads((self.p.root / ".cursor/hooks.json").read_text())
        for event, rows in config["hooks"].items():
            for row in rows:
                self.assertEqual(row.get("failClosed", False), event == "preToolUse", event)
                self.assertIn("scripts/cursor-hook.py", row["command"])
                self.assertIn(event, row["command"].split())


class SkillNudgeTests(Base):
    def nudge(self, path, **kw):
        return self.p.fire_one("preToolUse", self.write_payload(path, **kw), "Write")

    def test_red_governed_path_is_denied_with_valid_cursor_json(self):
        value = out_json(self, self.nudge(str(self.p.root / "api/fleet/x.proto")))
        self.assertIn("fleet-contract-rigor", self.deny_message(value))

    def test_green_ungoverned_path_is_allowed(self):
        self.assertEqual(out_json(self, self.nudge(str(self.p.root / "docs/design.md"))), {"permission": "allow"})

    def test_relative_path_is_resolved_against_the_payload_cwd(self):
        value = out_json(self, self.nudge("web/src/App.tsx"))
        self.assertIn("fleet-frontend", self.deny_message(value))

    def test_write_path_field_names(self):
        for key in ("file_path", "path", "target_file", "filePath", "file"):
            value = out_json(self, self.nudge(str(self.p.root / "api/fleet/x.proto"), key=key))
            self.assertEqual(value["permission"], "deny", key)
        top = {"tool_name": "Write", "tool_input": {}, "cwd": str(self.p.root),
               "file_path": str(self.p.root / "api/fleet/x.proto")}
        value = out_json(self, self.p.fire_one("preToolUse", top, "Write"))
        self.assertEqual(value["permission"], "deny", "top-level file_path")

    def test_write_without_a_path(self):
        result = self.p.fire_one("preToolUse", {"tool_name": "Write", "tool_input": {"contents": "x"}}, "Write")
        self.assertEqual(out_json(self, result), {"permission": "allow"})
        self.assertIn("no file path", result.stderr)

    def test_reading_the_skill_satisfies_the_nudge(self):
        proto = str(self.p.root / "api/fleet/x.proto")
        skill = str(self.p.root / ".agents/skills/fleet-contract-rigor/SKILL.md")
        read = {"tool_name": "Read", "tool_input": {"file_path": skill}, "tool_output": "..."}
        self.assertEqual(out_json(self, self.p.fire_one("postToolUse", read, "Read")), {})
        self.assertEqual(out_json(self, self.nudge(proto)), {"permission": "allow"})

    def test_reading_another_file_does_not_satisfy_it(self):
        read = {"tool_name": "Read", "tool_input": {"file_path": str(self.p.root / "docs/design.md")}}
        self.assertEqual(out_json(self, self.p.fire_one("postToolUse", read, "Read")), {})
        value = out_json(self, self.nudge(str(self.p.root / "api/fleet/x.proto")))
        self.assertEqual(value["permission"], "deny")

    def test_hooks_off_switch_reaches_the_script(self):
        data = json.dumps(self.write_payload(str(self.p.root / "api/fleet/x.proto"), conversation_id=CONVO))
        result = self.p.adapter("preToolUse", self.p.root / ".claude/hooks/skill-nudge.sh", data, FLEET_HOOKS_OFF="1")
        self.assertEqual(out_json(self, result), {"permission": "allow"})


class GuardFailureTests(Base):
    nudge_script = ".claude/hooks/skill-nudge.sh"

    def test_missing_guard_script_fails_closed(self):
        result = self.p.adapter("preToolUse", self.p.root / "nowhere.sh", self.write_payload("a.go"))
        self.assertIn("nowhere.sh", self.deny_message(out_json(self, result)))

    def test_unreadable_payload_fails_closed_for_a_guard(self):
        result = self.p.adapter("preToolUse", self.p.root / self.nudge_script, "not json")
        self.assertEqual(out_json(self, result)["permission"], "deny")

    def test_guard_script_crash_fails_closed(self):
        crash = self.p.stub("crash.sh", "echo boom >&2; exit 1\n")
        self.assertIn("boom", self.deny_message(out_json(self, self.p.adapter("preToolUse", crash, self.write_payload("a.go")))))

    def test_exit_2_message_is_never_lost(self):
        refuse = self.p.stub("refuse.sh", "echo 'BLOCKED: nope' >&2; exit 2\n")
        message = self.deny_message(out_json(self, self.p.adapter("beforeShellExecution", refuse,
                                                                    {"command": "rm -rf /", "cwd": str(self.p.root)})))
        self.assertIn("BLOCKED: nope", message)

    def test_a_permission_deny_on_stdout_is_a_deny(self):
        deny = self.p.stub("deny.sh", 'echo \'{"hookSpecificOutput":{"permissionDecision":"deny",'
                                      '"permissionDecisionReason":"policy says no"}}\'\n')
        value = out_json(self, self.p.adapter("beforeShellExecution", deny, {"command": "ls"}))
        self.assertIn("policy says no", self.deny_message(value))

    def test_clean_shell_command_is_allowed(self):
        ok = self.p.stub("ok.sh", "exit 0\n")
        value = out_json(self, self.p.adapter("beforeShellExecution", ok, {"command": "ls"}))
        self.assertEqual(value, {"permission": "allow"})

    def test_advisory_hooks_fail_open(self):
        for script in (self.p.root / "nowhere.sh", self.p.stub("crash.sh", "exit 1\n")):
            result = self.p.adapter("postToolUse", script, {"tool_name": "Write", "tool_input": {}})
            self.assertEqual(out_json(self, result), {})
            self.assertNotEqual(result.stderr, "")
        self.assertEqual(out_json(self, self.p.adapter("stop", self.p.root / "nowhere.sh", {})), {})
        self.assertEqual(out_json(self, self.p.adapter("beforeSubmitPrompt", self.p.root / "x.sh", {"prompt": "hi"})),
                         {"continue": True})

    def test_exit_2_on_an_event_without_a_permission_field_stays_exit_2(self):
        refuse = self.p.stub("refuse.sh", "echo 'stop it' >&2; exit 2\n")
        result = self.p.adapter("stop", refuse, {"status": "completed"})
        self.assertEqual(result.returncode, 2)
        self.assertIn("stop it", result.stderr)


class PayloadShapeTests(Base):
    """The Claude-shaped payload each event hands to the hook script."""

    def seen_for(self, event, payload):
        stub = self.p.recording_stub()
        self.assertEqual(self.p.adapter(event, stub, dict(payload, conversation_id=CONVO)).returncode, 0)
        return self.p.seen()

    def test_before_shell_execution(self):
        seen, _ = self.seen_for("beforeShellExecution", {"command": "make gates", "cwd": "/w", "sandbox": False})
        self.assertEqual(seen, {"tool_name": "Bash", "tool_input": {"command": "make gates"}, "cwd": "/w",
                                "session_id": CONVO, "hook_event_name": "PreToolUse"})

    def test_pre_tool_use_write(self):
        seen, _ = self.seen_for("preToolUse", {"tool_name": "Write", "tool_input": {"path": "/w/a.go"}, "cwd": "/w"})
        self.assertEqual(seen, {"tool_name": "Write", "tool_input": {"file_path": "/w/a.go"}, "cwd": "/w",
                                "session_id": CONVO, "hook_event_name": "PreToolUse"})

    def test_pre_tool_use_for_another_tool_is_not_run(self):
        stub = self.p.recording_stub()
        result = self.p.adapter("preToolUse", stub, {"tool_name": "Shell", "tool_input": {"command": "ls"}})
        self.assertEqual(out_json(self, result), {"permission": "allow"})
        self.assertFalse((self.p.tmp / "seen.json").exists())

    def test_after_file_edit(self):
        seen, _ = self.seen_for("afterFileEdit", {"file_path": "/w/a.go", "edits": []})
        self.assertEqual(seen["tool_name"], "Edit")
        self.assertEqual(seen["tool_input"], {"file_path": "/w/a.go"})
        self.assertEqual(seen["hook_event_name"], "PostToolUse")
        self.assertEqual(seen["session_id"], CONVO)

    def test_post_tool_use_write_and_read_skill(self):
        seen, _ = self.seen_for("postToolUse", {"tool_name": "Write", "tool_input": {"file_path": "/w/a.go"}})
        self.assertEqual((seen["tool_name"], seen["tool_input"], seen["hook_event_name"]),
                         ("Write", {"file_path": "/w/a.go"}, "PostToolUse"))
        seen, _ = self.seen_for("postToolUse", {"tool_name": "Read", "tool_input": {"path": str(self.p.root / ".cursor/skills/fleet-go/SKILL.md")}})
        self.assertEqual((seen["tool_name"], seen["tool_input"]), ("Skill", {"skill": "fleet-go"}))

    def test_session_start_and_pre_compact_sources(self):
        seen, _ = self.seen_for("sessionStart", {"session_id": "s", "is_background_agent": False})
        self.assertEqual((seen["hook_event_name"], seen["source"]), ("SessionStart", "startup"))
        seen, _ = self.seen_for("preCompact", {"trigger": "auto"})
        self.assertEqual((seen["hook_event_name"], seen["source"]), ("SessionStart", "compact"))

    def test_before_submit_prompt(self):
        seen, _ = self.seen_for("beforeSubmitPrompt", {"prompt": "hello", "attachments": []})
        self.assertEqual((seen["hook_event_name"], seen["prompt"], seen["session_id"]),
                         ("UserPromptSubmit", "hello", CONVO))

    def test_stop_loop_count_becomes_stop_hook_active(self):
        for count, active in ((0, False), (1, True), (4, True)):
            seen, _ = self.seen_for("stop", {"status": "completed", "loop_count": count})
            self.assertEqual((seen["hook_event_name"], seen["stop_hook_active"]), ("Stop", active), count)

    def test_cwd_and_project_dir_fall_back_to_the_project_root(self):
        seen, project = self.seen_for("stop", {"status": "completed", "loop_count": 0})
        self.assertEqual(seen["cwd"], str(self.p.root))
        self.assertEqual(project, str(self.p.root))

    def test_an_existing_claude_project_dir_is_kept(self):
        stub = self.p.recording_stub()
        self.p.adapter("stop", stub, {"status": "completed", "loop_count": 0}, CLAUDE_PROJECT_DIR="/elsewhere")
        self.assertEqual(self.p.seen()[1], "/elsewhere")

    def test_workspace_root_is_used_when_the_env_is_missing(self):
        stub = self.p.recording_stub()
        env = self.p.env()
        env.pop("CURSOR_PROJECT_DIR")
        subprocess.run([sys.executable, str(ADAPTER), "stop", str(stub)], capture_output=True, text=True, env=env,
                       input=json.dumps({"status": "completed", "loop_count": 0, "workspace_roots": ["/ws"]}), cwd="/")
        self.assertEqual(self.p.seen()[0]["cwd"], "/ws")


class OutputMappingTests(Base):
    def emitting(self, text):
        return self.p.stub("emit.sh", "cat >/dev/null; cat <<'EOF'\n%s\nEOF\n" % text)

    def hso(self, event, text):
        return json.dumps({"hookSpecificOutput": {"hookEventName": event, "additionalContext": text}})

    def test_context_goes_where_each_event_accepts_it(self):
        ctx = self.emitting(self.hso("X", "remember this"))
        cases = (("postToolUse", {"tool_name": "Write", "tool_input": {}}, {"additional_context": "remember this"}),
                 ("afterFileEdit", {"file_path": "a"}, {"additional_context": "remember this"}),
                 ("sessionStart", {}, {"additional_context": "remember this"}),
                 ("preCompact", {}, {"user_message": "remember this"}),
                 ("beforeSubmitPrompt", {"prompt": "x"}, {"continue": True}))
        for event, payload, want in cases:
            self.assertEqual(out_json(self, self.p.adapter(event, ctx, payload)), want, event)

    def test_plain_stdout_text_counts_as_context(self):
        value = out_json(self, self.p.adapter("sessionStart", self.emitting("just words"), {}))
        self.assertEqual(value, {"additional_context": "just words"})

    def test_stop_block_becomes_a_followup_message(self):
        block = self.emitting('{"decision":"block","reason":"keep going"}')
        self.assertEqual(out_json(self, self.p.adapter("stop", block, {"status": "completed", "loop_count": 0})), {"followup_message": "keep going"})

    def test_silence_is_an_empty_object(self):
        quiet = self.p.stub("quiet.sh", "cat >/dev/null\n")
        for event in ("stop", "preCompact", "postToolUse", "sessionStart"):
            self.assertEqual(out_json(self, self.p.adapter(event, quiet, {"tool_name": "Write", "tool_input": {}})), {}, event)

    def test_child_stdout_is_never_passed_through_raw(self):
        noisy = self.p.stub("noisy.sh", "cat >/dev/null; echo '<<< not json'\n")
        value = out_json(self, self.p.adapter("beforeShellExecution", noisy, {"command": "ls"}))
        self.assertEqual(value["permission"], "allow")


class StopTests(Base):
    def stop(self, loop_count=0):
        return out_json(self, self.p.fire_one("stop", {"status": "completed", "loop_count": loop_count}))

    def test_red_unfinished_goal_sends_a_followup(self):
        self.p.goal("Finish the migration")
        value = self.stop()
        self.assertIn("Finish the migration", value["followup_message"])
        self.assertIn("arbiter-fleet", value["followup_message"])

    def test_green_the_loop_breaker_done_and_no_goal_allow_the_stop(self):
        self.assertEqual(self.stop(), {})
        self.p.goal("Finish the migration")
        self.assertEqual(self.stop(loop_count=1), {})
        self.p.goal("DONE\nFinish the migration")
        self.assertEqual(self.stop(), {})

    def test_another_conversations_goal_does_not_bind(self):
        self.p.goal("Finish the migration", sid="someone-else")
        self.assertEqual(self.stop(), {})


class PromptAndCompactTests(Base):
    def submit(self, prompt):
        return out_json(self, self.p.fire_one("beforeSubmitPrompt", {"prompt": prompt, "attachments": []}))

    def test_a_goal_prompt_writes_the_goal_file_the_stop_hook_reads(self):
        value = self.submit("/goal consent push -- Ship the dev hooks")
        self.assertEqual(value["continue"], True)
        goal = (self.p.root / ".claude" / ("active-goal." + CONVO)).read_text()
        self.assertTrue(goal.startswith("SESSION: " + CONVO))
        self.assertIn("Ship the dev hooks", goal)
        self.assertIn("Ship the dev hooks", out_json(self, self.p.fire_one("stop", {"status": "completed", "loop_count": 0}))["followup_message"])

    def test_a_plain_prompt_is_let_through_untouched(self):
        self.assertEqual(self.submit("how do I run the tests?"), {"continue": True})
        self.assertFalse((self.p.root / ".claude" / ("active-goal." + CONVO)).exists())

    def test_the_restated_goal_is_dropped_because_cursor_shows_user_message_only_on_a_block(self):
        self.p.goal("Finish the migration")
        self.assertEqual(self.submit("status?"), {"continue": True})

    def test_pre_compact_clears_the_loaded_skill_markers(self):
        proto = str(self.p.root / "api/fleet/x.proto")
        skill = str(self.p.root / ".agents/skills/fleet-contract-rigor/SKILL.md")
        self.p.fire_one("postToolUse", {"tool_name": "Read", "tool_input": {"file_path": skill}}, "Read")
        nudge = lambda: out_json(self, self.p.fire_one("preToolUse", {"tool_name": "Write", "tool_input": {"file_path": proto}}, "Write"))
        self.assertEqual(nudge(), {"permission": "allow"})
        self.p.goal("Finish the migration")
        value = out_json(self, self.p.fire_one("preCompact", {"trigger": "auto"}))
        self.assertIn("AGENTS.md", value["user_message"])
        self.assertIn("fleet-contract-rigor", value["user_message"])
        self.assertIn("Finish the migration", value["user_message"])
        self.assertEqual(nudge()["permission"], "deny")

    def test_session_start_is_silent_on_startup(self):
        on_compact = self.p.root / ".claude/hooks/on-compact.sh"
        self.p.goal("Finish the migration")
        self.assertEqual(out_json(self, self.p.adapter("sessionStart", on_compact, {"conversation_id": CONVO})), {})


class DocStalenessTests(Base):
    def seed_go_source(self):
        pkg = self.p.root / "internal/foo"
        pkg.mkdir(parents=True)
        (pkg / "foo.go").write_text("package foo\n")

    def edit(self):
        return out_json(self, self.p.fire_one("postToolUse", {"tool_name": "Write", "tool_input": {
            "file_path": str(self.p.root / "internal/foo/foo.go")}, "cwd": str(self.p.root)}, "Write"))

    def test_red_changed_go_source_without_package_docs(self):
        self.seed_go_source()
        value = self.edit()
        self.assertIn("doc-staleness: internal/foo: missing doc.go", value["additional_context"])

    def test_green_nothing_to_report(self):
        self.assertEqual(self.edit(), {})

    def test_the_same_reminder_is_not_repeated(self):
        self.seed_go_source()
        self.assertIn("additional_context", self.edit())
        self.assertEqual(self.edit(), {})

    def test_after_file_edit_maps_the_same_reminder(self):
        self.seed_go_source()
        script = self.p.root / ".claude/hooks/doc-staleness.sh"
        value = out_json(self, self.p.adapter("afterFileEdit", script, {
            "conversation_id": CONVO, "file_path": str(self.p.root / "internal/foo/foo.go"), "edits": []}))
        self.assertIn("missing doc.go", value["additional_context"])


if __name__ == "__main__":
    unittest.main(verbosity=1)
