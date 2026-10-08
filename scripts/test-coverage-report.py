#!/usr/bin/env python3
"""Seeded cases for scripts/coverage-report.py (#107, a report only).

Each case writes a small Go coverage profile and runs the real script on it.
The expected percentages are worked out by hand in the comments.
"""
from __future__ import annotations

import os
import subprocess
import sys
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
SCRIPT = os.path.join(HERE, "coverage-report.py")
MOD = "example.com/m"


def run(profile_text: str | None, *args: str) -> subprocess.CompletedProcess:
    with tempfile.TemporaryDirectory() as d:
        with open(os.path.join(d, "go.mod"), "w") as f:
            f.write(f"module {MOD}\n\ngo 1.27\n")
        path = os.path.join(d, "cover.out")
        if profile_text is not None:
            with open(path, "w") as f:
                f.write(profile_text)
        return subprocess.run(
            [sys.executable, SCRIPT, path, *args],
            cwd=d, capture_output=True, text=True,
        )


def rows(out: str) -> dict[str, list[str]]:
    got = {}
    for line in out.splitlines():
        if line.startswith("| ") and not line.startswith(("| Package", "|---")):
            cells = [c.strip() for c in line.strip("|").split("|")]
            got[cells[0]] = cells[1:]
    return got


class CoverageReportTests(unittest.TestCase):
    def check(self, text: str | None, want_rc: int = 0, *args: str) -> str:
        p = run(text, *args)
        self.assertEqual(want_rc, p.returncode, f"stdout:\n{p.stdout}\nstderr:\n{p.stderr}")
        return p.stdout

    def test_percent_per_package_and_total(self):
        # a: 3 of 4 statements covered (75%); b: 0 of 2 (0%); all: 3 of 6 (50%).
        out = self.check(
            "mode: set\n"
            f"{MOD}/a/a.go:3.1,5.2 3 1\n"
            f"{MOD}/a/a.go:7.1,8.2 1 0\n"
            f"{MOD}/b/b.go:3.1,4.2 2 0\n"
        )
        got = rows(out)
        self.assertEqual(["75.0%", "4"], got["`a`"])
        self.assertEqual(["0.0%", "2"], got["`b`"])
        self.assertEqual(["50.0%", "6"], got["all listed"])

    def test_a_block_seen_twice_counts_once_and_covered_if_any_run_hit_it(self):
        # The same block twice (count 0, then 4): 2 of 2 covered, not 2 of 4.
        out = self.check(
            "mode: atomic\n"
            f"{MOD}/a/a.go:3.1,5.2 2 0\n"
            f"{MOD}/a/a.go:3.1,5.2 2 4\n"
        )
        self.assertEqual(["100.0%", "2"], rows(out)["`a`"])

    def test_generated_code_is_left_out(self):
        out = self.check(
            "mode: set\n"
            f"{MOD}/gen/go/x/v1/x.pb.go:3.1,5.2 50 0\n"
            f"{MOD}/a/a.go:3.1,5.2 1 1\n"
        )
        got = rows(out)
        self.assertEqual(["`a`", "all listed"], list(got))
        self.assertEqual(["100.0%", "1"], got["all listed"])

    def test_package_outside_the_module_keeps_its_full_path(self):
        out = self.check("mode: set\nother.org/x/y.go:1.1,2.2 1 1\n")
        self.assertIn("`other.org/x`", rows(out))

    def test_rows_are_sorted_by_package(self):
        out = self.check(
            "mode: set\n"
            f"{MOD}/z/z.go:1.1,2.2 1 1\n"
            f"{MOD}/a/a.go:1.1,2.2 1 1\n"
        )
        self.assertEqual(["`a`", "`z`", "all listed"], list(rows(out)))

    def test_no_profile_is_a_note_not_a_failure(self):
        out = self.check(None)
        self.assertIn("No coverage profile", out)
        self.assertEqual({}, rows(out))

    def test_a_profile_with_no_statements_says_so(self):
        out = self.check("mode: set\n")
        self.assertIn("no statements", out)
        self.assertEqual({}, rows(out))

    def test_not_a_profile_exits_2(self):
        for text in [
            "",
            "hello\n",
            f"{MOD}/a/a.go:3.1,5.2 3 1\n",
            f"mode: set\n{MOD}/a/a.go:3.1,5.2 three 1\n",
            "mode: set\nnot a block\n",
        ]:
            with self.subTest(text=text):
                p = run(text)
                self.assertEqual(2, p.returncode, p.stdout)
                self.assertEqual("", p.stdout)

    def test_a_package_name_with_markdown_is_escaped(self):
        out = self.check("mode: set\nweird.org/a|b/x.go:1.1,2.2 1 1\n")
        self.assertIn("weird.org/a\\|b", out)


if __name__ == "__main__":
    unittest.main()
