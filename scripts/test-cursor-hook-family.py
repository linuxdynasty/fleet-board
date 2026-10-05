#!/usr/bin/env python3
"""A parent and its helper agents share one skill-marker key; the opt-in payload log.

Cursor runs a helper agent (Task tool) under its own ids, so a SKILL.md read by
a helper must unlock the parent's governed edit and the reverse, as under
Claude Code, where a subagent keeps the parent's session_id. `subagentStart`
records the link; every event then resolves its key to the root of the family.
Which id a helper's own tool hooks carry is not known from the bundle, so each
case carries it as conversation_id, as session_id and as subagent_id.
Fixtures: cursor_hook_testkit.py. Worktree reads: test-cursor-hook-worktrees.py.
"""
from __future__ import annotations

import json
import os
import subprocess
import sys
import unittest
from pathlib import Path

from cursor_hook_testkit import CONVO, Base, Project, out_json

PROTO = "api/fleet/x.proto"
SKILL = ".agents/skills/fleet-contract-rigor/SKILL.md"
HELPER, SUB, UNLINKED, OTHER = "helper-0000000001", "sub-00000000002", "unlinked-00000003", "other-00000000004"
LINK_PREFIX = "fleet-cursor-link-"
ROOT_PREFIX = "fleet-cursor-root-"
ALLOW = '{"permission": "allow"}\n'
SCRIPTS = os.path.dirname(os.path.abspath(__file__))

# (what subagentStart carries, what the helper's own tool hooks carry)
START_BY_CONVERSATION = {"subagent_id": SUB, "conversation_id": HELPER, "parent_conversation_id": CONVO}
START_PARENT_CONVERSATION = {"subagent_id": SUB, "conversation_id": CONVO, "parent_conversation_id": CONVO}
CASES = {
    "helper id as conversation_id": (START_BY_CONVERSATION, {"conversation_id": HELPER}),
    "helper id as session_id": (START_BY_CONVERSATION, {"conversation_id": UNLINKED, "session_id": HELPER}),
    "helper id as subagent_id": (START_BY_CONVERSATION, {"conversation_id": UNLINKED, "subagent_id": SUB}),
    "subagent_id as subagent_id, start conversation is the parent's":
        (START_PARENT_CONVERSATION, {"conversation_id": UNLINKED, "subagent_id": SUB}),
    "subagent_id as session_id, start conversation is the parent's":
        (START_PARENT_CONVERSATION, {"conversation_id": UNLINKED, "session_id": SUB}),
}
CASES["session_id shared with the parent, all other ids unknown"] = (
    START_PARENT_CONVERSATION, {"conversation_id": UNLINKED, "session_id": CONVO})
# A start that carries its own session id PS (not the parent's conversation id), and a helper whose later events
# carry PS beside a conversation id that was never announced.
PS, FRESH = "psess-0000000005", "fresh-0000000006"
START_WITH_SESSION = {"subagent_id": SUB, "conversation_id": CONVO, "session_id": PS,
                      "parent_conversation_id": CONVO}
CASES["start session_id PS, start conversation is the parent's"] = (
    START_WITH_SESSION, {"conversation_id": FRESH, "session_id": PS})
CASES["start session_id PS, start conversation is the helper's"] = (
    dict(START_WITH_SESSION, conversation_id=HELPER), {"conversation_id": FRESH, "session_id": PS})
PARENT = {"conversation_id": CONVO, "session_id": CONVO}


class FamilyBase(Base):
    def fresh(self):
        self.p = Project()
        self.addCleanup(self.p.cleanup)

    def start(self, **fields):
        result = self.p.fire_as("subagentStart", fields)
        self.assertEqual((result.returncode, result.stdout), (0, ALLOW), result.stderr)

    def read_skill(self, ids):
        payload = dict(ids, tool_name="Read", tool_input={"file_path": str(self.p.root / SKILL)}, cwd=str(self.p.root))
        self.assertEqual(out_json(self, self.p.fire_as("postToolUse", payload, "Read")), {})

    def edit(self, ids):
        payload = dict(ids, tool_name="Write", tool_input={"file_path": str(self.p.root / PROTO)})
        return out_json(self, self.p.fire_as("preToolUse", payload, "Write"))["permission"]

    def link_file(self, child, parent):
        (self.p.state / (LINK_PREFIX + child)).write_text(parent)


class WiringTests(FamilyBase):
    def test_subagent_start_output_is_exactly_allow(self):
        for fields in (START_BY_CONVERSATION, {}, {"subagent_id": 7}):
            result = self.p.fire_as("subagentStart", fields)
            self.assertEqual((result.returncode, result.stdout), (0, ALLOW), fields)

    def test_unreadable_subagent_start_payloads_are_allowed_too(self):
        row = self.p.commands("subagentStart")[0]["command"]
        for data in ("not json", "[]", "[" * 200000 + "]" * 200000, ""):
            result = self.p.adapter("subagentStart", "", data)
            self.assertEqual((result.returncode, result.stdout), (0, ALLOW), data[:20])
        self.assertNotIn("failClosed", self.p.commands("subagentStart")[0])
        self.assertIn("subagentStart", row.split())


class HelperSharingTests(FamilyBase):
    def test_a_helper_read_unlocks_the_parents_edit(self):
        for name, (start, helper) in CASES.items():
            with self.subTest(name):
                self.fresh()
                self.assertEqual(self.edit(PARENT), "deny")
                self.start(**start)
                self.read_skill(dict(helper))
                self.assertEqual(self.edit(PARENT), "allow")
                self.assertEqual(self.edit(helper), "allow")

    def test_the_parents_read_unlocks_the_helpers_edit(self):
        for name, (start, helper) in CASES.items():
            with self.subTest(name):
                self.fresh()
                self.start(**start)
                self.assertEqual(self.edit(helper), "deny")
                self.read_skill(PARENT)
                self.assertEqual(self.edit(helper), "allow")

    def test_a_nested_helper_shares_with_the_root(self):
        self.start(subagent_id="sub-a", conversation_id="conv-a-0000001", parent_conversation_id=CONVO)
        self.start(subagent_id="sub-b", conversation_id="conv-b-0000001", parent_conversation_id="conv-a-0000001")
        self.read_skill({"conversation_id": "conv-b-0000001"})
        self.assertEqual(self.edit(PARENT), "allow")
        self.assertEqual(self.edit({"conversation_id": "conv-a-0000001"}), "allow")

    def test_unlinked_conversations_do_not_share_markers(self):
        self.start(**START_BY_CONVERSATION)
        self.read_skill({"conversation_id": OTHER})
        self.assertEqual(self.edit(PARENT), "deny")
        self.assertEqual(self.edit({"conversation_id": HELPER}), "deny")
        self.assertEqual(self.edit({"conversation_id": OTHER}), "allow")
        self.assertEqual(self.edit({"conversation_id": UNLINKED}), "deny")

    def test_an_unrelated_chat_with_its_own_session_id_does_not_share(self):
        self.start(**START_WITH_SESSION)
        stranger = {"conversation_id": OTHER, "session_id": "psess-other-0007"}
        self.read_skill(stranger)
        self.assertEqual(self.edit(PARENT), "deny")
        self.assertEqual(self.edit({"conversation_id": FRESH, "session_id": PS}), "deny")
        self.assertEqual(self.edit(stranger), "allow")

    def test_a_link_to_one_parent_does_not_leak_to_siblings_chats(self):
        self.start(**START_BY_CONVERSATION)
        self.read_skill({"conversation_id": HELPER})
        self.assertEqual(self.edit({"conversation_id": OTHER}), "deny")

    def test_without_any_link_a_conversation_keeps_its_own_key(self):
        self.read_skill({"conversation_id": CONVO})
        self.assertEqual(self.edit({"session_id": CONVO, "conversation_id": CONVO}), "allow")
        self.assertEqual(self.edit({"conversation_id": HELPER}), "deny")
        self.read_skill({"session_id": OTHER})  # only a session_id: the key is that id
        self.assertEqual(self.edit({"session_id": OTHER}), "allow")


class LinkSafetyTests(FamilyBase):
    def test_a_link_cycle_is_cut_when_written(self):
        self.start(subagent_id="loop-a", conversation_id="loop-a", parent_conversation_id="loop-b")
        self.start(subagent_id="loop-b", conversation_id="loop-b", parent_conversation_id="loop-a")
        self.assertEqual([p.name for p in self.p.state.glob(LINK_PREFIX + "*")], [LINK_PREFIX + "loop-a"])

    def test_a_planted_cycle_ends_safely_and_agrees_on_one_key(self):
        self.link_file("loop-a", "loop-b")
        self.link_file("loop-b", "loop-c")
        self.link_file("loop-c", "loop-a")
        self.read_skill({"conversation_id": "loop-a"})
        for member in ("loop-a", "loop-b", "loop-c"):
            self.assertEqual(self.edit({"conversation_id": member}), "allow", member)
        self.assertEqual(self.edit({"conversation_id": OTHER}), "deny")
        self.link_file("self-link", "self-link")
        self.read_skill({"conversation_id": "self-link"})
        self.assertEqual(self.edit({"conversation_id": "self-link"}), "allow")

    def chain(self, length):
        previous = CONVO
        for depth in range(1, length + 1):
            self.link_file("deep-%02d" % depth, previous)
            previous = "deep-%02d" % depth
        return previous

    def test_a_chain_within_the_bound_reaches_the_root(self):
        self.read_skill({"conversation_id": self.chain(4)})
        self.assertEqual(self.edit(PARENT), "allow")

    def test_an_over_deep_chain_ends_safely_short_of_the_root(self):
        leaf = self.chain(15)
        self.read_skill({"conversation_id": leaf})
        self.assertEqual(self.edit({"conversation_id": leaf}), "allow")  # still answers
        self.assertEqual(self.edit(PARENT), "deny")  # the bound stopped short of the root

    def test_malformed_start_payloads_never_block_and_write_nothing(self):
        bad_ids = ["../escape", "a/b", "..", "a..b", "", "x" * 1000, 5, ["a"], {"a": 1}, None, "a\nb", "a\x00b",
                   " lead"]
        before = self.tree()
        for bad in bad_ids:
            for fields in ({"subagent_id": bad, "conversation_id": bad, "parent_conversation_id": CONVO},
                           {"subagent_id": SUB, "conversation_id": SUB, "parent_conversation_id": bad}):
                result = self.p.fire_as("subagentStart", fields)
                self.assertEqual((result.returncode, result.stdout), (0, ALLOW), repr(fields))
        self.assertEqual(self.tree(), before)

    def test_a_valid_link_writes_one_small_file_inside_the_state_dir_only(self):
        before = self.tree()
        self.start(**START_BY_CONVERSATION)
        names = sorted(self.tree() - before)
        links = [str(self.p.state / (LINK_PREFIX + n)) for n in (HELPER, SUB)]
        self.assertEqual(names, sorted(links + [str(self.p.state / (ROOT_PREFIX + CONVO))]))
        for name in links:
            self.assertEqual(Path(name).read_text().strip(), CONVO)
        for name in names:
            self.assertLess(os.path.getsize(name), 200)
        self.assertEqual([n for n in os.listdir(self.p.state) if n.endswith(".tmp")], [])

    def test_an_unwritable_state_dir_is_noted_and_still_allowed(self):
        self.p.state.chmod(0o500)
        self.addCleanup(self.p.state.chmod, 0o700)
        result = self.p.fire_as("subagentStart", START_BY_CONVERSATION)
        self.assertEqual((result.returncode, result.stdout), (0, ALLOW))

    def tree(self):
        return {os.path.join(d, f) for d, _, files in os.walk(self.p.tmp) for f in files}


class EventScopeTests(FamilyBase):
    """Only the skill events share a key. Claude never fires Stop, UserPromptSubmit or SessionStart for a subagent."""

    def helper_stop(self):
        payload = {"conversation_id": HELPER, "status": "completed", "loop_count": 0}
        return out_json(self, self.p.fire_as("stop", payload))

    def test_a_helpers_completed_stop_does_not_get_the_parents_goal(self):
        self.start(**START_BY_CONVERSATION)
        self.p.goal("Finish the migration")
        self.assertEqual(self.helper_stop(), {})
        parent = out_json(self, self.p.fire_as("stop", dict(PARENT, status="completed", loop_count=0)))
        self.assertIn("Finish the migration", parent["followup_message"])

    def test_a_helpers_goal_done_leaves_the_parents_goal_active(self):
        self.start(**START_BY_CONVERSATION)
        self.p.goal("Finish the migration")
        submit = {"conversation_id": HELPER, "prompt": "/goal done"}
        value = out_json(self, self.p.fire_as("beforeSubmitPrompt", submit))
        self.assertEqual(value, {"continue": True})
        goal = (self.p.root / ".claude" / ("active-goal." + CONVO)).read_text()
        self.assertFalse(goal.startswith("DONE"), goal)
        self.assertIn("followup_message", out_json(self, self.p.fire_as("stop", dict(PARENT, status="completed",
                                                                                      loop_count=0))))

    def test_a_helpers_compaction_leaves_the_familys_skill_markers(self):
        self.start(**START_BY_CONVERSATION)
        self.read_skill(PARENT)
        self.p.fire_as("preCompact", {"conversation_id": HELPER})
        self.assertEqual(self.edit(PARENT), "allow")
        self.assertEqual(self.edit({"conversation_id": HELPER}), "allow")

    def test_a_helper_event_with_the_parents_session_id_still_keeps_the_chat_key(self):
        """conversation_id comes first, so a helper that carries the parent's session_id is still its own chat."""
        self.start(**START_BY_CONVERSATION)
        self.p.goal("Finish the migration")
        ids = {"conversation_id": HELPER, "session_id": CONVO}
        self.assertEqual(out_json(self, self.p.fire_as("stop", dict(ids, status="completed", loop_count=0))), {})
        out_json(self, self.p.fire_as("beforeSubmitPrompt", dict(ids, prompt="/goal done")))
        goal = (self.p.root / ".claude" / ("active-goal." + CONVO)).read_text()
        self.assertFalse(goal.startswith("DONE"), goal)
        self.assertIn("followup_message", out_json(self, self.p.fire_as("stop", dict(PARENT, status="completed",
                                                                                      loop_count=0))))

    def test_the_parents_own_compaction_still_clears_the_markers(self):
        self.start(**START_BY_CONVERSATION)
        self.read_skill({"conversation_id": HELPER})
        self.p.fire_as("preCompact", PARENT)
        self.assertEqual(self.edit(PARENT), "deny")
        self.assertEqual(self.edit({"conversation_id": HELPER}), "deny")


class StateFileTests(FamilyBase):
    """Link and root files are written and read defensively: the state dir may be a shared /tmp."""

    def run_start_as_pid(self, pid, fields):
        """The adapter's subagentStart with os.getpid() fixed, so a scratch file name can be guessed."""
        code = ("import os, runpy, sys\nos.getpid = lambda: %d\nsys.path.insert(0, %r)\n"
                "sys.argv = ['cursor-hook.py', 'subagentStart']\n"
                "runpy.run_path(%r, run_name='__main__')\n"
                % (pid, SCRIPTS, str(self.p.root / "scripts/cursor-hook.py")))
        return subprocess.run([sys.executable, "-c", code], input=json.dumps(fields), capture_output=True, text=True,
                              env=self.p.env(), cwd=self.p.root)

    def victim(self):
        victim = self.p.tmp / "victim"
        victim.write_text("keep me")
        return victim

    def test_a_symlink_planted_at_the_scratch_name_is_not_followed(self):
        victim = self.victim()
        (self.p.state / (".%s%s.4242.tmp" % (LINK_PREFIX, HELPER))).symlink_to(victim)
        result = self.run_start_as_pid(4242, START_BY_CONVERSATION)
        self.assertEqual((result.returncode, result.stdout), (0, ALLOW), result.stderr)
        self.assertEqual(victim.read_text(), "keep me")
        self.assertIn("cannot record", result.stderr)

    def test_a_symlink_or_directory_at_the_destination_is_noted_and_left_alone(self):
        victim = self.victim()
        (self.p.state / (LINK_PREFIX + HELPER)).symlink_to(victim)
        (self.p.state / (ROOT_PREFIX + CONVO)).mkdir()
        result = self.p.fire_as("subagentStart", START_BY_CONVERSATION)
        self.assertEqual((result.returncode, result.stdout), (0, ALLOW))
        self.assertEqual(victim.read_text(), "keep me")
        self.assertIn("not a regular file", result.stderr)
        self.assertIn(ROOT_PREFIX + CONVO, result.stderr)

    def test_a_symlinked_link_file_is_ignored(self):
        real = self.p.tmp / "real-link"
        real.write_text(CONVO + "\n")
        (self.p.state / (LINK_PREFIX + HELPER)).symlink_to(real)
        self.read_skill(PARENT)
        self.assertEqual(self.edit({"conversation_id": HELPER}), "deny")
        (self.p.state / (LINK_PREFIX + HELPER)).unlink()
        self.link_file(HELPER, CONVO + "\n")  # the same content as a regular file does link
        self.assertEqual(self.edit({"conversation_id": HELPER}), "allow")

    def test_a_link_file_holding_a_bad_parent_is_ignored(self):
        self.read_skill(PARENT)
        for content in ("../x", CONVO + "\nextra", "x" * 300, "a/b", "", "  \n", "..", CONVO + "/.."):
            self.link_file(HELPER, content)
            self.assertEqual(self.edit({"conversation_id": HELPER}), "deny", repr(content))

    def test_a_crash_while_recording_still_answers_allow(self):
        code = ("import runpy, sys\nsys.path.insert(0, %r)\nimport cursor_hook_family as family\n"
                "family.record_links = lambda payload: 1 / 0\nsys.argv = ['cursor-hook.py', 'subagentStart']\n"
                "runpy.run_path(%r, run_name='__main__')\n" % (SCRIPTS, str(self.p.root / "scripts/cursor-hook.py")))
        result = subprocess.run([sys.executable, "-c", code], input=json.dumps(START_BY_CONVERSATION),
                                capture_output=True, text=True, env=self.p.env(), cwd=self.p.root)
        self.assertEqual((result.returncode, result.stdout), (0, ALLOW), result.stderr)
        self.assertIn("ZeroDivisionError", result.stderr)


class MissingModuleTests(FamilyBase):
    """The adapter needs scripts/cursor_hook_family.py. Without a usable one it must still print one JSON object."""

    def solo(self, module=None):
        """The adapter alone in its own scripts dir, with no module, or with `module` as the module's text."""
        scripts = self.p.tmp / "solo" / "scripts"
        scripts.mkdir(parents=True, exist_ok=True)
        for old in scripts.iterdir():
            old.unlink()
        (scripts / "cursor-hook.py").write_text((self.p.root / "scripts/cursor-hook.py").read_text())
        if module is not None:
            (scripts / "cursor_hook_family.py").write_text(module)
        return scripts / "cursor-hook.py"

    def run_solo(self, adapter, event, payload):
        script = str(self.p.root / ".claude/hooks/skill-nudge.sh")
        return subprocess.run([sys.executable, str(adapter), event, script], input=json.dumps(payload),
                              capture_output=True, text=True, env=self.p.env(), cwd=self.p.root)

    def check(self, adapter):
        write = {"tool_name": "Write", "tool_input": {"file_path": "a.go"}}
        for event in ("preToolUse", "beforeShellExecution"):
            result = self.run_solo(adapter, event, dict(write, command="ls"))
            value = out_json(self, result)
            self.assertEqual(value["permission"], "deny", event)
            self.assertIn("cursor_hook_family", value["agent_message"])
            self.assertIn("cursor_hook_family", value["user_message"])
        result = self.run_solo(adapter, "subagentStart", START_BY_CONVERSATION)
        self.assertEqual((result.returncode, result.stdout), (0, ALLOW), result.stderr)
        advisory = (("postToolUse", write, {}), ("stop", {"status": "completed"}, {}), ("preCompact", {}, {}),
                    ("beforeSubmitPrompt", {"prompt": "x"}, {"continue": True}))
        for event, payload, want in advisory:
            self.assertEqual(out_json(self, self.run_solo(adapter, event, payload)), want, event)
        self.assertIn("cursor_hook_family", self.run_solo(adapter, "stop", {}).stderr)

    def test_a_missing_module(self):
        self.check(self.solo())

    def test_a_module_with_a_syntax_error(self):
        self.check(self.solo((self.p.root / "scripts/cursor_hook_family.py").read_text() + "\n)(\n"))

    def test_a_module_that_raises_when_imported(self):
        self.check(self.solo("raise RuntimeError('boom')\n"))

    def test_the_adapter_with_its_module_is_not_affected(self):
        adapter = self.solo((self.p.root / "scripts/cursor_hook_family.py").read_text())
        result = self.run_solo(adapter, "subagentStart", START_BY_CONVERSATION)
        self.assertEqual((result.returncode, result.stdout, result.stderr), (0, ALLOW, ""))


class PayloadLogTests(FamilyBase):
    def log(self):
        return self.p.tmp / "hook.log"

    def lines(self):
        return [json.loads(line) for line in self.log().read_text().splitlines()]

    def run_event(self, event, payload, **env):
        stub = self.p.stub("quiet.sh", "/bin/cat >/dev/null\n")
        return self.p.adapter(event, stub, payload, **env)

    def test_only_the_allowed_fields_are_written_and_the_key_is_the_resolved_one(self):
        env = {"FLEET_CURSOR_HOOK_LOG": str(self.log())}
        self.p.adapter("subagentStart", "", dict(START_BY_CONVERSATION, task="SECRET-TASK", model="m"), **env)
        payload = {"tool_name": "Read", "tool_input": {"file_path": "/home/me/SECRET-PATH"}, "conversation_id": HELPER,
                   "session_id": "s-1", "subagent_id": SUB, "prompt": "SECRET-PROMPT", "command": "SECRET-CMD"}
        self.run_event("postToolUse", payload, **env)
        start, post = self.lines()
        keys = ["conversation_id", "model", "parent_conversation_id", "subagent_id", "task"]
        self.assertEqual(start, {"event": "subagentStart", "keys": keys,
                                 "conversation_id": HELPER, "session_id": None, "subagent_id": SUB,
                                 "parent_conversation_id": CONVO, "resolved_key": CONVO})
        self.assertEqual(post["keys"], sorted(payload))
        self.assertEqual([post[k] for k in ("event", "conversation_id", "session_id", "subagent_id",
                                            "parent_conversation_id", "resolved_key")],
                         ["postToolUse", HELPER, "s-1", SUB, None, CONVO])
        self.assertEqual(sorted(post), sorted(start))
        text = self.log().read_text()
        for secret in ("SECRET-TASK", "SECRET-PATH", "SECRET-PROMPT", "SECRET-CMD"):
            self.assertNotIn(secret, text)

    def test_nothing_is_written_when_the_variable_is_unset(self):
        self.run_event("postToolUse", {"tool_name": "Write", "tool_input": {}, "conversation_id": CONVO})
        self.p.adapter("subagentStart", "", START_BY_CONVERSATION)
        self.assertEqual([p.name for p in self.p.tmp.rglob("*.log")], [])
        self.assertFalse(self.log().exists())

    def test_non_string_ids_are_written_as_null_and_values_are_cut(self):
        env = {"FLEET_CURSOR_HOOK_LOG": str(self.log())}
        payload = {"status": "completed", "conversation_id": 12, "session_id": "s" * 5000, "subagent_id": ["x"]}
        self.run_event("stop", payload, **env)
        (row,) = self.lines()
        self.assertIsNone(row["conversation_id"])
        self.assertIsNone(row["subagent_id"])
        self.assertLessEqual(len(row["session_id"]), 200)

    def test_an_unwritable_log_does_not_change_the_answer(self):
        env = {"FLEET_CURSOR_HOOK_LOG": str(self.p.tmp / "no-such-dir" / "hook.log")}
        result = self.p.adapter("subagentStart", "", START_BY_CONVERSATION, **env)
        self.assertEqual((result.returncode, result.stdout), (0, ALLOW))
        self.assertIn("hook log", result.stderr)


class HintTests(FamilyBase):
    def test_the_deny_note_says_a_worktree_copy_counts(self):
        payload = {"tool_name": "Write", "tool_input": {"file_path": str(self.p.root / PROTO)}}
        value = out_json(self, self.p.fire_one("preToolUse", payload, "Write"))
        self.assertIn("worktree you are editing counts too", value["agent_message"])
        self.assertNotIn("worktree", value["user_message"])

    def test_the_compaction_note_names_the_files_and_the_worktree_rule(self):
        self.read_skill({"conversation_id": CONVO})
        self.p.goal("Finish the migration")
        value = out_json(self, self.p.fire_one("preCompact", {"trigger": "auto"}))
        self.assertIn(".agents/skills/fleet-contract-rigor/SKILL.md", value["user_message"])
        self.assertIn("worktree you are editing counts too", value["user_message"])

    def test_a_compaction_with_no_loaded_skills_gets_no_hint(self):
        value = out_json(self, self.p.fire_one("preCompact", {"trigger": "auto"}))
        self.assertNotIn("worktree you are editing", value.get("user_message", ""))


if __name__ == "__main__":
    unittest.main(verbosity=1)
