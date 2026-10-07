#!/usr/bin/env python3
"""Print a Go coverage profile as a markdown table, one row per package.

Usage: scripts/coverage-report.py PROFILE      (run from the module root)

CI passes the profile `make test` or `make test-all` wrote (COVERPROFILE) and
appends the output to the job summary (#107). A report only: there is no
minimum and no rule against a drop. Each row is the share of the package's
statements that at least one test run executed. A block listed more than once
(the same file and range) counts once, covered if any listing has a count
above 0. Generated code under gen/ is left out. A missing profile prints a
note and exits 0 (no package was tested, or go test did not start); a run
with failing tests still leaves a profile, so its table shows. Input that is
not a coverage profile exits 2.
"""
from __future__ import annotations

import os
import re
import sys

BLOCK_RE = re.compile(r"^(.+):(\d+\.\d+,\d+\.\d+) (\d+) (\d+)$")
MODE_RE = re.compile(r"^mode: (set|count|atomic)$")
HEADER = "### Test coverage (report)\n"


class BadInput(Exception):
    pass


def module_path() -> str:
    try:
        with open("go.mod") as f:
            for line in f:
                parts = line.split()
                if len(parts) == 2 and parts[0] == "module":
                    return parts[1]
    except OSError:
        pass
    return ""


def parse(text: str) -> dict[str, dict[str, tuple[int, bool]]]:
    """Package -> block key -> (statements, covered)."""
    lines = text.splitlines()
    if not lines or not MODE_RE.match(lines[0]):
        raise BadInput("the first line is not 'mode: set|count|atomic'")
    pkgs: dict[str, dict[str, tuple[int, bool]]] = {}
    for n, line in enumerate(lines[1:], start=2):
        if not line:
            continue
        m = BLOCK_RE.match(line)
        if not m:
            raise BadInput(f"line {n} is not a coverage block: {line!r}")
        path, rng, stmts, count = m.group(1), m.group(2), int(m.group(3)), int(m.group(4))
        blocks = pkgs.setdefault(os.path.dirname(path), {})
        key = f"{path}:{rng}"
        _, seen = blocks.get(key, (stmts, False))
        blocks[key] = (stmts, seen or count > 0)
    return pkgs


def pct(covered: int, total: int) -> str:
    return f"{100.0 * covered / total:.1f}%"


def main() -> int:
    if len(sys.argv) != 2:
        print("usage: coverage-report.py PROFILE", file=sys.stderr)
        return 2
    try:
        with open(sys.argv[1]) as f:
            text = f.read()
    except FileNotFoundError:
        print(HEADER)
        print("No coverage profile: no Go package was tested, or go test did not start.")
        return 0
    try:
        pkgs = parse(text)
    except BadInput as exc:
        print(f"coverage-report: {exc}", file=sys.stderr)
        return 2

    mod = module_path()
    rows, all_cov, all_total = [], 0, 0
    for pkg in sorted(pkgs):
        rel = pkg[len(mod) + 1:] if mod and pkg.startswith(mod + "/") else pkg
        if rel == "gen" or rel.startswith("gen/"):
            continue
        blocks = pkgs[pkg].values()
        total = sum(s for s, _ in blocks)
        if total == 0:
            continue
        cov = sum(s for s, c in blocks if c)
        all_cov, all_total = all_cov + cov, all_total + total
        name = rel.replace("|", "\\|")
        rows.append(f"| `{name}` | {pct(cov, total)} | {total} |")

    print(HEADER)
    if not rows:
        print("The profile has no statements outside gen/; nothing to report.")
        return 0
    print(
        f"Share of statements the tests ran, in the {len(rows)} packages tested "
        "(generated code in gen/ left out). A report: there is no minimum.\n"
    )
    print("| Package | Covered | Statements |")
    print("|---|---|---|")
    print("\n".join(rows))
    print(f"| all listed | {pct(all_cov, all_total)} | {all_total} |")
    return 0


if __name__ == "__main__":
    sys.exit(main())
