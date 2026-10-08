#!/usr/bin/env python3
"""Seeded RED/GREEN cases for scripts/bench-review.py.

Each case feeds a benchstat -format csv text (the shape the pinned benchstat
writes: one table per unit, a geomean row, blank lines between tables) to the
real script and checks the markdown it prints. A row is marked REVIEW when
time per op rose more than 10% with p < 0.05, or allocs per op rose with
p < 0.05 (benchstat writes `~` instead of a delta when p >= 0.05). Findings
exit 0; input that is not a benchstat CSV exits 2.
"""
from __future__ import annotations

import subprocess
import sys
import unittest
from pathlib import Path

SCRIPT = Path(__file__).resolve().parent / "bench-review.py"

PREAMBLE = "goos: linux\ngoarch: amd64\npkg: example.com/m/store\ncpu: test\n"


def table(unit: str, rows: list[str]) -> str:
    head = ",base,,head,,,\n" + f",{unit},CI,{unit},CI,vs base,P\n"
    return head + "\n".join(rows) + "\ngeomean,1,,1,,+0.00%,\n\n"


def run(csv: str) -> subprocess.CompletedProcess:
    return subprocess.run([sys.executable, str(SCRIPT)], input=csv, capture_output=True, text=True, check=False)


def review_lines(out: str) -> list[str]:
    return [line for line in out.splitlines() if line.startswith("|") and "REVIEW" in line]


class BenchReviewTests(unittest.TestCase):
    def check(self, csv: str, want_rc: int = 0) -> str:
        res = run(csv)
        self.assertEqual(want_rc, res.returncode, f"stdout:\n{res.stdout}\nstderr:\n{res.stderr}")
        return res.stdout

    def test_a_a_run_has_no_review(self):
        out = self.check(PREAMBLE + table("sec/op", [
            "WriterBatch/jobs=1-4,0.0038,1%,0.0039,2%,~,p=0.310 n=10",
            "WriterBatch/jobs=100-4,0.0045,3%,0.0044,4%,~,p=0.180 n=10",
        ]) + table("allocs/op", [
            "WriterBatch/jobs=1-4,32,3%,32,3%,~,p=1.000 n=10",
        ]))
        self.assertEqual([], review_lines(out))
        self.assertIn("0 marked REVIEW", out)
        self.assertIn("WriterBatch/jobs=100-4", out)

    def test_time_rise_over_ten_percent_with_low_p_is_review(self):
        out = self.check(PREAMBLE + table("sec/op", [
            "WriterBatch/jobs=1-4,0.0038,1%,0.0044,1%,+15.79%,p=0.000 n=10",
        ]))
        rows = review_lines(out)
        self.assertEqual(1, len(rows), out)
        self.assertIn("WriterBatch/jobs=1-4", rows[0])
        self.assertIn("+15.79%", rows[0])
        self.assertIn("1 marked REVIEW", out)

    def test_time_rise_under_ten_percent_is_not_review(self):
        out = self.check(PREAMBLE + table("sec/op", [
            "WriterBatch/jobs=1-4,0.0038,1%,0.0041,1%,+7.89%,p=0.000 n=10",
        ]))
        self.assertEqual([], review_lines(out))

    def test_time_rise_without_significance_is_not_review(self):
        out = self.check(PREAMBLE + table("sec/op", [
            "WriterBatch/jobs=1-4,0.0038,30%,0.0050,30%,~,p=0.089 n=10",
        ]))
        self.assertEqual([], review_lines(out))

    def test_time_drop_is_not_review(self):
        out = self.check(PREAMBLE + table("sec/op", [
            "WriterBatch/jobs=1-4,0.0050,1%,0.0038,1%,-24.00%,p=0.000 n=10",
        ]))
        self.assertEqual([], review_lines(out))

    def test_any_significant_alloc_rise_is_review(self):
        out = self.check(PREAMBLE + table("allocs/op", [
            "WriterBatch/jobs=1-4,32,0%,33,0%,+3.12%,p=0.000 n=10",
            "WriterBatch/jobs=10-4,249,0%,248,0%,-0.40%,p=0.000 n=10",
        ]))
        rows = review_lines(out)
        self.assertEqual(1, len(rows), out)
        self.assertIn("jobs=1-4", rows[0])

    def test_alloc_rise_from_zero_is_review(self):
        # benchstat prints `?` for the change when the base is 0.
        out = self.check(PREAMBLE + table("allocs/op", [
            "Work-4,0,0%,1,0%,?,p=0.002 n=6",
            "Same-4,0,0%,0,0%,~,p=1.000 n=6",
        ]))
        rows = review_lines(out)
        self.assertEqual(1, len(rows))
        self.assertIn("Work-4", rows[0])

    def test_bytes_and_custom_units_are_reported_never_review(self):
        out = self.check(PREAMBLE + table("B/op", [
            "WriterBatch/jobs=1-4,976,0%,2000,0%,+104.92%,p=0.000 n=10",
        ]) + table("jobs/batch", [
            "WriterBatch/jobs=1-4,1,0%,1,0%,~,p=1.000 n=10",
        ]))
        self.assertEqual([], review_lines(out))
        self.assertIn("+104.92%", out)

    def test_bytes_print_in_binary_units(self):
        out = self.check(PREAMBLE + table("B/op", [
            "WriterBatch/jobs=1-4,972,0%,972,0%,~,p=1.000 n=10",
            "WriterBatch/jobs=100-4,56300,0%,56300,0%,~,p=1.000 n=10",
            "WriterBatch/big-4,3145728,0%,3145728,0%,~,p=1.000 n=10",
        ]))
        self.assertIn("| 972 B | 972 B |", out)
        self.assertIn("| 54.98 KiB | 54.98 KiB |", out)
        self.assertIn("| 3 MiB | 3 MiB |", out)

    def test_new_and_removed_benchmarks_are_named_not_review(self):
        out = self.check(PREAMBLE + table("sec/op", [
            "WriterBatch/jobs=100-4,,,0.0044,4%",
            "WriterBatch/jobs=7-4,0.0044,4%",
        ]))
        self.assertEqual([], review_lines(out))
        self.assertRegex(out, r"jobs=100-4.*new")
        self.assertRegex(out, r"jobs=7-4.*removed")

    def test_head_only_table_lists_every_row_as_new(self):
        out = self.check(PREAMBLE + ",head,\n,sec/op,CI\nWriterBatch/jobs=1-4,0.0038,1%\ngeomean,0.0038,\n\n")
        self.assertEqual([], review_lines(out))
        self.assertRegex(out, r"jobs=1-4.*new")

    def test_a_cell_with_markdown_is_escaped(self):
        out = self.check(PREAMBLE + table("sec/op", [
            "Weird|name-4,0.0038,1%,0.0039,2%,~,p=0.310 n=10",
        ]))
        self.assertIn("Weird\\|name-4", out)

    def test_not_benchstat_csv_exits_2(self):
        for text in ["", "hello\n", "goos: linux\n", ",base,,head,,,\n,sec/op,CI\nx,1\n"]:
            with self.subTest(text=text):
                res = run(text)
                self.assertEqual(2, res.returncode, res.stdout + res.stderr)


if __name__ == "__main__":
    unittest.main()
