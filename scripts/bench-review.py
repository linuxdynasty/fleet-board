#!/usr/bin/env python3
"""Turn a benchstat -format csv report into a markdown table with REVIEW marks.

Reads the CSV on stdin (scripts/bench-report.sh passes the pinned benchstat's
output) and prints markdown for the CI step summary (#107, a report only).
A row is marked REVIEW when, with p < 0.05:
  - time per op (sec/op) rose more than 10%, or
  - allocations per op (allocs/op) rose at all.
benchstat writes `~` instead of a change when p >= 0.05, so `~` is never
REVIEW. Other units (B/op, custom metrics) are listed and never marked. A
benchmark only in head is "new", only in base "removed". REVIEW asks a person
to look; it is not a failure, so findings exit 0. Input that is not a benchstat
CSV exits 2.
"""
from __future__ import annotations

import csv
import re
import sys

TIME_LIMIT_PCT = 10.0
ALPHA = 0.05
P_RE = re.compile(r"p=([0-9.]+)")
DELTA_RE = re.compile(r"^([+-][0-9.]+)%$")


class BadInput(Exception):
    pass


def tables(text: str):
    """Yield (unit, has_base, rows) per benchstat table; rows are CSV lists."""
    lines = text.splitlines()
    i, found = 0, 0
    while i < len(lines):
        line = lines[i]
        if not (line.startswith(",") and "head" in line.split(",")):
            i += 1
            continue
        if i + 1 >= len(lines):
            raise BadInput("a table header has no unit line")
        units = next(csv.reader([lines[i + 1]]))
        if len(units) != len(next(csv.reader([line]))):
            raise BadInput(f"the unit line does not match its header: {lines[i + 1]!r}")
        if len(units) == 3 and units[2] == "CI":
            has_base = False
        elif len(units) == 7 and units[2] == "CI" and units[5] == "vs base" and units[6] == "P":
            has_base = True
        else:
            raise BadInput(f"unexpected unit line: {lines[i + 1]!r}")
        rows, i = [], i + 2
        while i < len(lines) and lines[i].strip():
            rows.append(next(csv.reader([lines[i]])))
            i += 1
        found += 1
        yield units[1], has_base, rows
    if not found:
        raise BadInput("no benchstat table found")


def human(unit: str, raw: str) -> str:
    if raw == "":
        return ""
    try:
        v = float(raw)
    except ValueError as exc:
        raise BadInput(f"not a number: {raw!r}") from exc
    if unit == "sec/op":
        for scale, name in ((1, "s"), (1e-3, "ms"), (1e-6, "µs")):
            if v >= scale:
                return f"{v / scale:.3g} {name}"
        return f"{v / 1e-9:.3g} ns"
    if unit == "B/op":
        for scale, name in ((1 << 20, "MiB"), (1 << 10, "KiB")):
            if v >= scale:
                return f"{v / scale:.4g} {name}"
        return f"{v:.4g} B"
    s = f"{v:.4g}"
    return f"{v:.0f}" if "e" in s else s


def verdict(unit: str, base: str, head: str, delta: str, p: str) -> str:
    pm = P_RE.search(p)
    if not pm or float(pm.group(1)) >= ALPHA:
        return ""
    # benchstat writes `?` for the change when the base is 0: no percent exists.
    if delta == "?":
        rose = float(head) > float(base)
        return "REVIEW" if rose and unit in ("sec/op", "allocs/op") else ""
    m = DELTA_RE.match(delta)
    if not m:
        return ""
    change = float(m.group(1))
    if unit == "sec/op" and change > TIME_LIMIT_PCT:
        return "REVIEW"
    if unit == "allocs/op" and change > 0:
        return "REVIEW"
    return ""


def cell(s: str) -> str:
    return s.replace("|", "\\|")


def review(text: str) -> tuple[list[str], int, int]:
    out, compared, marked = [], 0, 0
    for unit, has_base, rows in tables(text):
        for r in rows:
            if not r or r[0] == "geomean":
                continue
            name = r[0]
            if not has_base:
                base, head, delta, p, mark = "", human(unit, r[1]), "", "", "new"
            elif len(r) >= 7:
                base, head, delta, p = human(unit, r[1]), human(unit, r[3]), r[5], r[6]
                mark = verdict(unit, r[1], r[3], delta, p)
                compared += 1
            elif len(r) == 5 and r[1] == "":
                base, head, delta, p, mark = "", human(unit, r[3]), "", "", "new"
            elif len(r) == 3:
                base, head, delta, p, mark = human(unit, r[1]), "", "", "", "removed"
            else:
                raise BadInput(f"unexpected row: {r!r}")
            marked += mark == "REVIEW"
            out.append("| " + " | ".join(cell(c) for c in (name, unit, base, head, delta, p, mark)) + " |")
    return out, compared, marked


def main() -> int:
    try:
        rows, compared, marked = review(sys.stdin.read())
    except BadInput as exc:
        print(f"bench-review: {exc}", file=sys.stderr)
        return 2
    print("### Benchmarks: base vs head\n")
    print(
        f"{compared} rows compared, {marked} marked REVIEW (time per op up more than "
        f"{TIME_LIMIT_PCT:g}%, or allocations per op up, each with p < {ALPHA:g}). "
        "REVIEW asks a person to look; it never fails the build.\n"
    )
    print("| Benchmark | Unit | Base | Head | Change | p | |")
    print("|---|---|---|---|---|---|---|")
    print("\n".join(rows))
    return 0


if __name__ == "__main__":
    sys.exit(main())
