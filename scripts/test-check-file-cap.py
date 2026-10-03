#!/usr/bin/env python3
"""Seeded RED/GREEN cases for scripts/check-file-cap.sh."""

import os
import subprocess
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
CHECK = ROOT / "scripts/check-file-cap.sh"


class FileCapTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.repo = Path(self.tmp.name)
        self.env = {k: v for k, v in os.environ.items() if not k.startswith("GIT_")}
        self.env["FLEET_FILE_CAP_REPO_ROOT"] = str(self.repo)
        subprocess.run(["git", "init", "-q", str(self.repo)], env=self.env, check=True)

    def add(self, name, lines):
        path = self.repo / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text("x\n" * lines)
        subprocess.run(["git", "-C", str(self.repo), "add", name], env=self.env, check=True)

    def check(self):
        return subprocess.run([str(CHECK)], env=self.env, capture_output=True, text=True)

    def test_under_the_cap_passes(self):
        self.add("a.md", 499)
        self.assertEqual(0, self.check().returncode)

    def test_at_the_cap_fails_and_is_named(self):
        self.add("b.md", 500)
        out = self.check()
        self.assertEqual(1, out.returncode)
        self.assertIn("b.md(500)", out.stderr)

    def test_generated_and_lock_files_are_skipped(self):
        for name in ("go.sum", "web/package-lock.json", "gen/go/x.go", "internal/p/testdata/big.txt"):
            self.add(name, 900)
        self.assertEqual(0, self.check().returncode)

    def test_symlinks_are_skipped(self):
        os.symlink(self.repo, self.repo / "link")
        subprocess.run(["git", "-C", str(self.repo), "add", "link"], env=self.env, check=True)
        self.assertEqual(0, self.check().returncode)


if __name__ == "__main__":
    unittest.main()
