#!/usr/bin/env python3
"""A SKILL.md read in a git worktree of the project's repo counts as a skill load.

Cursor is often opened on the main checkout while the agent works in a worktree
(the repo's required workflow), so the worktree's own copy of a skill has to
count. Only worktrees that share the project's git common dir do; other repos,
clones, symlinks out, unknown ids and `..` escapes stay rejected, and when git
is missing or fails the project-only rule applies. Real `git worktree`s, in a
temp repo. Fixtures: cursor_hook_testkit.py.
"""
from __future__ import annotations

import os
import subprocess
import unittest
from pathlib import Path

from cursor_hook_testkit import Base, out_json

SKILL_ID = "fleet-contract-rigor"
SKILL = ".agents/skills/%s/SKILL.md" % SKILL_ID
PROTO = "api/fleet/x.proto"
IDENT = ["-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"]


def git(cwd, *args):
    subprocess.run(["git", *IDENT, *args], cwd=str(cwd), check=True, capture_output=True, text=True)


def seed_skill(root):
    skill = Path(root) / SKILL
    skill.parent.mkdir(parents=True, exist_ok=True)
    skill.write_text("---\nname: %s\n---\n" % SKILL_ID)


class WorktreeBase(Base):
    def setUp(self):
        super().setUp()
        seed_skill(self.p.root)
        git(self.p.root, "add", ".agents")
        git(self.p.root, "commit", "-q", "-m", "seed")
        self.wt = self.p.tmp / "wt2"
        git(self.p.root, "worktree", "add", "-q", "-b", "wt2", str(self.wt))

    def read(self, path, cwd=None, **env):
        payload = {"tool_name": "Read", "tool_input": {"file_path": str(path)}, "conversation_id": "convo-1234567890",
                   "cwd": str(cwd or self.p.root)}
        result = self.p.adapter("postToolUse", self.p.root / ".claude/hooks/skill-loaded.sh", payload, **env)
        self.assertEqual(out_json(self, result), {})

    def edit_permission(self, root):
        payload = {"tool_name": "Write", "tool_input": {"file_path": str(root / PROTO)}, "cwd": str(root),
                   "conversation_id": "convo-1234567890"}
        result = self.p.adapter("preToolUse", self.p.root / ".claude/hooks/skill-nudge.sh", payload)
        return out_json(self, result)["permission"]

    def other_repo(self, name, clone_of=None):
        path = self.p.tmp / name
        if clone_of:
            git(self.p.tmp, "clone", "-q", str(clone_of), str(path))
        else:
            path.mkdir()
            git(path, "init", "-q")
            seed_skill(path)
            (path / ".agents/skill-paths.json").write_text((self.p.root / ".agents/skill-paths.json").read_text())
            git(path, "add", ".agents")
            git(path, "commit", "-q", "-m", "seed")
        return path

    def markers(self):
        return sorted(p.name for p in self.p.state.glob("fleet-skill-loaded-*"))


class SiblingWorktreeTests(WorktreeBase):
    def test_a_read_in_a_second_worktree_unlocks_an_edit_there(self):
        self.assertEqual(self.edit_permission(self.wt), "deny")
        self.read(self.wt / SKILL, cwd=self.wt)
        self.assertEqual(self.edit_permission(self.wt), "allow")

    def test_it_counts_through_hooks_json_with_the_project_on_the_first_checkout(self):
        payload = {"tool_name": "Read", "tool_input": {"file_path": str(self.wt / SKILL)}, "cwd": str(self.wt)}
        self.assertEqual(out_json(self, self.p.fire_one("postToolUse", payload, "Read")), {})
        proto = {"tool_name": "Write", "tool_input": {"file_path": str(self.wt / PROTO)}}
        self.assertEqual(out_json(self, self.p.fire_one("preToolUse", proto, "Write"))["permission"], "allow")

    def test_the_project_may_itself_be_the_worktree_and_the_read_in_the_main_checkout(self):
        payload = {"tool_name": "Read", "tool_input": {"file_path": str(self.p.root / SKILL)},
                   "conversation_id": "convo-1234567890", "cwd": str(self.wt)}
        result = self.p.adapter("postToolUse", self.p.root / ".claude/hooks/skill-loaded.sh", payload,
                                CURSOR_PROJECT_DIR=str(self.wt))
        self.assertEqual(out_json(self, result), {})
        self.assertEqual(self.edit_permission(self.wt), "allow")

    def test_a_skill_dir_symlinked_to_agents_skills_in_the_worktree_counts(self):
        (self.wt / ".claude/skills").mkdir(parents=True)
        (self.wt / ".claude/skills" / SKILL_ID).symlink_to(Path("../../.agents/skills") / SKILL_ID)
        self.read(self.wt / ".claude/skills" / SKILL_ID / "SKILL.md", cwd=self.wt)
        self.assertEqual(self.edit_permission(self.wt), "allow")

    def test_git_variables_in_the_environment_do_not_hide_the_worktrees(self):
        self.read(self.wt / SKILL, cwd=self.wt, GIT_DIR=str(self.p.tmp / "bogus"), GIT_WORK_TREE=str(self.p.tmp))
        self.assertEqual(self.edit_permission(self.wt), "allow")

    def test_an_id_the_worktree_does_not_have_does_not_count(self):
        unknown = self.wt / ".cursor/skills/fleet-not-a-skill/SKILL.md"
        unknown.parent.mkdir(parents=True)
        unknown.write_text("x")
        self.read(unknown, cwd=self.wt)
        self.assertEqual(self.markers(), [])
        # the id exists only as a plain file outside .agents/skills in that worktree
        (self.wt / ".agents/skills" / SKILL_ID / "SKILL.md").unlink()
        copy = self.wt / ".cursor/skills" / SKILL_ID / "SKILL.md"
        copy.parent.mkdir(parents=True)
        copy.write_text("x")
        self.read(copy, cwd=self.wt)
        self.assertEqual(self.edit_permission(self.wt), "deny")

    def test_other_places_in_a_worktree_do_not_count(self):
        docs = self.wt / "docs/skills" / SKILL_ID / "SKILL.md"
        docs.parent.mkdir(parents=True)
        docs.write_text("x")
        escape = self.wt / ".agents/skills/x/../../../../elsewhere/skills" / SKILL_ID / "SKILL.md"
        for path in (docs, escape, Path("/etc/skills") / SKILL_ID / "SKILL.md"):
            self.read(path, cwd=self.wt)
            self.assertEqual(self.edit_permission(self.wt), "deny", str(path))

    def test_a_skill_dir_symlinked_out_of_the_worktree_does_not_count(self):
        outside = self.p.tmp / "elsewhere" / SKILL_ID
        outside.mkdir(parents=True)
        (outside / "SKILL.md").write_text("x")
        (self.wt / ".cursor/skills").mkdir(parents=True)
        (self.wt / ".cursor/skills" / SKILL_ID).symlink_to(outside)
        self.read(self.wt / ".cursor/skills" / SKILL_ID / "SKILL.md", cwd=self.wt)
        self.assertEqual(self.edit_permission(self.wt), "deny")


class UnrelatedRepoTests(WorktreeBase):
    def test_another_repo_with_the_same_skill_id_does_not_count(self):
        other = self.other_repo("other")
        self.read(other / SKILL, cwd=other)
        self.assertEqual(self.edit_permission(other), "deny")
        self.assertEqual(self.edit_permission(self.p.root), "deny")

    def test_a_clone_is_a_different_repo_and_does_not_count(self):
        clone = self.other_repo("clone", clone_of=self.p.root)
        self.read(clone / SKILL, cwd=clone)
        self.assertEqual(self.edit_permission(clone), "deny")

    def test_the_project_dir_still_counts_as_before(self):
        self.read(self.p.root / SKILL)
        self.assertEqual(self.edit_permission(self.p.root), "allow")


class GitUnavailableTests(WorktreeBase):
    """With git missing or failing the old, project-only rule applies and the nudge keeps blocking."""

    def recorded(self, path, **env):
        stub = self.p.recording_stub()
        payload = {"tool_name": "Read", "tool_input": {"file_path": str(path)}, "cwd": str(self.p.root)}
        result = self.p.adapter("postToolUse", stub, payload, **env)
        self.assertEqual(out_json(self, result), {})
        return (self.p.tmp / "seen.json").exists() and self.p.seen()[0]["tool_input"] == {"skill": SKILL_ID}

    def test_with_git_on_the_path_the_sibling_read_is_a_skill_load(self):
        self.assertTrue(self.recorded(self.wt / SKILL))

    def test_with_no_git_at_all(self):
        nowhere = self.p.tmp / "empty-bin"
        nowhere.mkdir()
        self.assertFalse(self.recorded(self.wt / SKILL, PATH=str(nowhere)))
        self.assertTrue(self.recorded(self.p.root / SKILL, PATH=str(nowhere)))

    def test_with_a_git_that_fails(self):
        fake = self.p.tmp / "fake-bin"
        fake.mkdir()
        # it even prints the worktree before failing: a failed git must not be believed
        (fake / "git").write_text("#!/bin/sh\necho 'worktree %s'\nexit 1\n" % self.wt)
        (fake / "git").chmod(0o755)
        path = str(fake) + os.pathsep + os.environ.get("PATH", "")
        self.assertFalse(self.recorded(self.wt / SKILL, PATH=path))
        self.assertTrue(self.recorded(self.p.root / SKILL, PATH=path))

    def test_with_a_project_dir_that_is_not_a_repo(self):
        bare = self.p.tmp / "not-a-repo"
        bare.mkdir()
        self.assertFalse(self.recorded(self.wt / SKILL, CURSOR_PROJECT_DIR=str(bare)))


if __name__ == "__main__":
    unittest.main(verbosity=1)
