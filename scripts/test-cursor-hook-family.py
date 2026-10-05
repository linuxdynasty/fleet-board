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
import unittest

from cursor_hook_testkit import CONVO, Base, Project, out_json

PROTO = "api/fleet/x.proto"
SKILL = ".agents/skills/fleet-contract-rigor/SKILL.md"
HELPER, SUB, UNLINKED, OTHER = "helper-0000000001", "sub-00000000002", "unlinked-00000003", "other-00000000004"
LINK_PREFIX = "fleet-cursor-link-"
ROOT_PREFIX = "fleet-cursor-root-"
ALLOW = '{"permission": "allow"}\n'

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

    def test_a_link_to_one_parent_does_not_leak_to_siblings_chats(self):
        self.start(**START_BY_CONVERSATION)
        self.read_skill({"conversation_id": HELPER})
        self.assertEqual(self.edit({"conversation_id": OTHER}), "deny")

    def test_compaction_by_a_helper_clears_the_familys_markers_as_under_claude(self):
        self.start(**START_BY_CONVERSATION)
        self.read_skill(PARENT)
        self.p.fire_as("preCompact", {"conversation_id": HELPER})
        self.assertEqual(self.edit(PARENT), "deny")

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
        bad_ids = ["../escape", "a/b", "..", "a..b", "", "x" * 1000, 5, ["a"], {"a": 1}, None, "a\nb", "a\x00b", " lead"]
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
            self.assertEqual(open(name).read().strip(), CONVO)
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
        self.assertEqual(start, {"event": "subagentStart", "keys": ["conversation_id", "model", "parent_conversation_id",
                                                                    "subagent_id", "task"],
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
        self.run_event("stop", {"status": "completed", "conversation_id": 12, "session_id": "s" * 5000, "subagent_id": ["x"]}, **env)
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
