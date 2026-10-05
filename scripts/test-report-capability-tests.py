#!/usr/bin/env python3
"""Seeded cases for scripts/report-capability-tests.py.

Each case builds a small tree (01's capability table plus test files) and runs
the report against it. Usage: python3 scripts/test-report-capability-tests.py
"""

import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

SCRIPT = Path(__file__).resolve().parent / "report-capability-tests.py"

TABLE = """# 01

| ID | Capability | Kind | Phase |
|---|---|---|---|
| A1 | Enroll | C | v1 |
| A10 | Something later | C | v1 |
| E9 | Pane mapping | S, E | v1 |
| K3 | Transcripts | S | v3 |
"""


class Tree:
    def __init__(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name)
        self.write("docs/specs/01-agent-capabilities.md", TABLE)

    def write(self, rel, text):
        path = self.root / rel
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text)

    def run(self):
        return subprocess.run(
            [sys.executable, str(SCRIPT), "--root", str(self.root)],
            capture_output=True, text=True, check=False)


def untested(out):
    for line in out.splitlines():
        if line.startswith("untested:"):
            return line.split(":", 1)[1].split()
    raise AssertionError("no 'untested:' line in:\n" + out)


class ReportTest(unittest.TestCase):
    def setUp(self):
        self.tree = Tree()
        self.addCleanup(self.tree.tmp.cleanup)

    def report(self):
        res = self.tree.run()
        self.assertEqual(res.returncode, 0, res.stdout + res.stderr)
        return untested(res.stdout)

    def test_no_tests_lists_every_v1_id_in_table_order(self):
        self.assertEqual(self.report(), ["A1", "A10", "E9"])

    def test_go_test_naming_an_id_removes_it(self):
        self.tree.write("internal/x/x_test.go", "package x\n\n// Covers E9.\n")
        self.assertEqual(self.report(), ["A1", "A10"])

    def test_web_tests_count(self):
        self.tree.write("web/src/pane.test.ts", "it('A10 shows', () => {});\n")
        self.tree.write("web/src/enroll.spec.tsx", "// A1\n")
        self.assertEqual(self.report(), ["E9"])

    def test_gate_self_tests_do_not_count(self):
        self.tree.write("scripts/test-foo.py", "CASES = ['A1', 'E9']\n")
        self.tree.write("scripts/test-bar.sh", "# A10\n")
        self.assertEqual(self.report(), ["A1", "A10", "E9"])

    def test_go_test_under_scripts_does_not_count(self):
        self.tree.write("scripts/tool/tool_test.go", "// A1\n")
        self.assertEqual(self.report(), ["A1", "A10", "E9"])

    def test_go_test_in_a_testdata_dir_does_not_count(self):
        self.tree.write("internal/x/testdata/case/case_test.go", "// E9\n")
        self.assertEqual(self.report(), ["A1", "A10", "E9"])

    def test_whole_word_only(self):
        self.tree.write("internal/x/x_test.go", "// A10 and XA1 and A1b and E9x\n")
        self.assertEqual(self.report(), ["A1", "E9"])

    def test_underscore_separates_words(self):
        self.tree.write("internal/x/x_test.go", "func TestEnroll_A1(t *testing.T) {}\n")
        self.assertEqual(self.report(), ["A10", "E9"])

    def test_non_test_files_and_testdata_do_not_count(self):
        self.tree.write("internal/x/x.go", "// A1\n")
        self.tree.write("scripts/testdata/case/a_test.go", "// A10\n")
        self.tree.write("web/node_modules/lib/lib.test.ts", "// E9\n")
        self.assertEqual(self.report(), ["A1", "A10", "E9"])

    def test_v3_id_is_never_reported(self):
        self.assertNotIn("K3", self.report())

    def test_summary_line_counts(self):
        self.tree.write("internal/x/x_test.go", "// E9\n")
        res = self.tree.run()
        self.assertIn("2 of 3 v1 capabilities have no product test naming them", res.stdout)

    def test_missing_table_exits_2(self):
        (self.tree.root / "docs/specs/01-agent-capabilities.md").unlink()
        res = self.tree.run()
        self.assertEqual(res.returncode, 2, res.stdout + res.stderr)
        self.assertIn("report-capability-tests: cannot read", res.stderr)

    def test_table_without_rows_exits_2(self):
        self.tree.write("docs/specs/01-agent-capabilities.md", "# 01\n\nno table\n")
        res = self.tree.run()
        self.assertEqual(res.returncode, 2, res.stdout + res.stderr)
        self.assertIn("no capability table rows", res.stderr)

    def test_unreadable_test_file_exits_2(self):
        self.tree.write("internal/x/x_test.go", "// E9\n")
        (self.tree.root / "internal/x/x_test.go").write_bytes(b"\xff\xfe\x00bad")
        res = self.tree.run()
        self.assertEqual(res.returncode, 2, res.stdout + res.stderr)
        self.assertIn("report-capability-tests: cannot read internal/x/x_test.go", res.stderr)


if __name__ == "__main__":
    unittest.main()
