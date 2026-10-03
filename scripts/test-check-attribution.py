#!/usr/bin/env python3
"""Seeded RED/GREEN cases for scripts/check-attribution.py."""

import os
import subprocess
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
CHECK = ROOT / "scripts/check-attribution.py"


def run(args, cwd, stdin=None):
    env = {k: v for k, v in os.environ.items() if not k.startswith("GIT_")}
    return subprocess.run(["python3", str(CHECK), *args], cwd=cwd, env=env,
                          input=stdin, capture_output=True, text=True)


class AttributionTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.repo = self.tmp.name
        self.env = {k: v for k, v in os.environ.items() if not k.startswith("GIT_")}
        self.git("init", "-q", "-b", "main")
        self.commit("base commit\n\nCo-Authored-By: Claude <noreply@anthropic.com>")  # old history is allowed
        self.git("branch", "origin-main")
        self.git("update-ref", "refs/remotes/origin/main", "HEAD")

    def git(self, *args):
        subprocess.run(["git", "-c", "user.name=T", "-c", "user.email=t@example.com", "-C", self.repo, *args],
                       env=self.env, check=True, capture_output=True)

    def commit(self, message):
        Path(self.repo, "f.txt").write_text(message)
        self.git("add", "f.txt")
        self.git("commit", "-q", "-m", message)

    def test_clean_range_passes_and_old_trailers_are_ignored(self):
        self.commit("docs: add a thing\n\nShort body.")
        out = run(["--root", self.repo], self.repo)
        self.assertEqual(0, out.returncode, out.stderr)

    def test_model_trailer_fails(self):
        self.commit("docs: add a thing\n\nCo-Authored-By: Claude Sonnet <noreply@anthropic.com>")
        out = run(["--root", self.repo], self.repo)
        self.assertEqual(1, out.returncode)
        self.assertIn("Co-Authored-By", out.stderr)

    def test_generated_with_footer_fails(self):
        self.commit("docs: add a thing\n\n\U0001F916 Generated with [Claude Code](https://claude.com/claude-code)")
        self.assertEqual(1, run(["--root", self.repo], self.repo).returncode)

    def test_human_coauthor_passes(self):
        self.commit("docs: add a thing\n\nCo-Authored-By: Pat Lee <pat@example.com>")
        self.assertEqual(0, run(["--root", self.repo], self.repo).returncode)

    def test_pr_body_from_stdin(self):
        self.assertEqual(0, run(["--stdin"], self.repo, "Adds the plan.\n").returncode)
        self.assertEqual(1, run(["--stdin"], self.repo, "Adds the plan.\n\nGenerated with Claude Code\n").returncode)

    def test_missing_base_is_said_out_loud(self):
        out = run(["--root", self.repo, "--range", "nope/main..HEAD"], self.repo)
        self.assertEqual(0, out.returncode)
        self.assertIn("no commit range", out.stdout)


if __name__ == "__main__":
    unittest.main()
