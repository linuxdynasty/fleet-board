#!/usr/bin/env python3
"""Write the GitHub issue body of each v1 plan row from the plan itself.

The plan (docs/plan/v1-implementation-plan.md) is the one home of each PR's
scope, dependencies, acceptance test, role and skills (ADR 0026). This script
reads the section 5 row table, the section 3 prep table and the section 10
id-to-issue table, and renders one issue body per PR row.

  --print ROW   print the body of one row (for example 22 or 2b)
  --check       fetch each row's issue with `gh` and list those that differ
                (exit 1 if any differ); add --diff to show the differences
  --apply       edit the differing issues with `gh issue edit` (the owner
                approves this; it writes to GitHub). A partial --apply is
                safe to re-run: issues already updated no longer differ, so
                the next run edits only the rest

Exit codes: 0 all issues match (or --apply finished); 1 some issues differ;
2 the script could not do its job (`gh` missing or failed, plan file missing,
or a plan table or header it expects was renamed).

Only PR rows are covered. Prep items (section 3, ids 0.x) keep their
hand-written issue bodies; they are read only to turn "0.12" into "#57 (0.12)".
"""

import argparse
import difflib
import json
import os
import posixpath
import re
import subprocess
import sys
import tempfile
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
PLAN = ROOT / "docs/plan/v1-implementation-plan.md"
REPO = "linuxdynasty/fleet-board"
BLOB = f"https://github.com/{REPO}/blob/main/"
PLAN_URL = BLOB + "docs/plan/v1-implementation-plan.md#5-the-pr-sequence"
CHECKLIST_URL = BLOB + "AGENTS.md#pre-ready-checklist"
PLAN_DIR = "docs/plan"  # relative links in plan cells resolve from here

ROW_ID = r"\d+[ab]?"
ROLE_RE = re.compile(r"\b((?:develop|review|debug|perf|scan|arbiter)-fleet(?:-deep)?)\b")


def split_row(line):
    """Split a markdown table row on pipes that are not inside backticks."""
    cells, cur, tick = [], "", False
    for ch in line.strip()[1:-1]:
        if ch == "`":
            tick = not tick
        if ch == "|" and not tick:
            cells.append(cur.strip())
            cur = ""
        else:
            cur += ch
    cells.append(cur.strip())
    return cells


def section(text, start, end):
    try:
        a = text.index(start)
        b = text.index(end, a)
    except ValueError:
        raise ValueError(f"plan header not found: {start!r} (or its end marker {end!r}); was a header renamed?") from None
    return text[a:b]


def table_rows(sec, id_pattern):
    """Yield the cells of every table row whose first cell matches id_pattern."""
    for line in sec.splitlines():
        if re.match(rf"^\| ({id_pattern}) \|", line):
            yield split_row(line)


def parse_plan(text):
    """Return (rows, prep_ids, issue_of). rows is an ordered dict of row id to cells."""
    rows = {}
    for cells in table_rows(section(text, "## 5. The PR sequence", "## 6."), ROW_ID):
        rows[cells[0]] = cells
    prep = [c[0] for c in table_rows(section(text, "## 3.", "## 4."), r"0\.\d+")]
    issue_of = parse_mapping(section(text, "## 10. Tracking", "\n**Who merges"), rows, prep)
    return rows, prep, issue_of


def expand_ids(spec, rows, prep):
    """Expand "1 to 17", "0.5 to 0.10" or a single id into the plan's ids, in order."""
    spec = re.sub(r"\s*\(.*?\)", "", spec).strip()
    m = re.fullmatch(r"(\S+) to (\S+)", spec)
    if not m:
        return [spec]
    lo, hi = m.groups()
    if lo.startswith("0."):
        return [f"0.{n}" for n in range(int(lo[2:]), int(hi[2:]) + 1)]
    plain = sorted((int(i) for i in rows if i.isdigit()))
    return [str(n) for n in plain if int(lo) <= n <= int(hi)]


def parse_mapping(sec, rows, prep):
    out = {}
    for line in sec.splitlines():
        if not line.startswith("| ") or line.startswith("| Plan ids") or line.startswith("|---"):
            continue
        ids_cell, issues_cell = split_row(line)[:2]
        ids = expand_ids(ids_cell, rows, prep)
        nums = [int(n) for n in re.findall(r"#(\d+)", issues_cell)]
        if " to " in issues_cell:
            nums = list(range(nums[0], nums[1] + 1))
        if len(ids) != len(nums):
            raise ValueError(f"plan section 10: {ids_cell!r} has {len(ids)} ids but {len(nums)} issues")
        out.update(zip(ids, nums))
    return out


def absolute_links(text):
    """Turn relative markdown links (resolved from docs/plan/) into blob URLs."""

    def fix(m):
        target = m.group(1)
        if re.match(r"^(https?:|#|mailto:)", target):
            return m.group(0)
        path, _, frag = target.partition("#")
        full = posixpath.normpath(posixpath.join(PLAN_DIR, path))
        return f"]({BLOB}{full}" + (f"#{frag}" if frag else "") + ")"

    return re.sub(r"\]\(([^)\s]+)\)", fix, text)


def render_deps(deps, rows, prep, issue_of):
    out = []
    for tok in re.split(r",\s*", deps.strip()):
        if not tok:
            continue
        if tok == "all above":
            tok = "all rows above it in the plan table"
        m = re.fullmatch(r"(\d+) to (\d+)", tok)
        ids = expand_ids(tok, rows, prep) if m else [tok]
        for i in ids:
            if i in issue_of and (i in rows or i in prep):
                ref = f"#{issue_of[i]}" + (f" ({i})" if i.startswith("0.") else "")
            else:
                ref = i  # unknown id: keep as text
            if ref not in out:
                out.append(ref)
    return ", ".join(out) if out else "Nothing."


def title_of(row_id, scope):
    m = re.match(r"\*\*(.+?)\*\*", scope)
    name = m.group(1).rstrip(":").strip() if m else scope[:60]
    return f"v1 PR {row_id}: {name}"


def render_row(row_id, rows, prep, issue_of):
    """Return (title, body) for one row."""
    cells = rows[row_id]
    _, scope, deps, accept, role, skills = cells[:6]
    scope_text = absolute_links(scope).replace("**", "")
    role_text = ROLE_RE.sub(r"`\1`", role)
    body = f"""PR {row_id} of the [v1 implementation plan]({PLAN_URL}).

## Scope
{scope_text}

## Depends on
{render_deps(deps, rows, prep, issue_of)}

## Acceptance test
{absolute_links(accept)}

## Role and skills
- Role: {role_text}
- Skills: {skills}

## Done when
The [Pre-Ready Checklist]({CHECKLIST_URL}) in AGENTS.md is met, and the owner has merged the PR.
"""
    return title_of(row_id, scope), body


def render_all(plan_path=PLAN):
    rows, prep, issue_of = parse_plan(Path(plan_path).read_text())
    return {r: render_row(r, rows, prep, issue_of) + (issue_of.get(r),) for r in rows}, rows


def gh_view(number):
    out = subprocess.run(
        ["gh", "issue", "view", str(number), "-R", REPO, "--json", "title,body"],
        check=True, capture_output=True, text=True,
    ).stdout
    return json.loads(out)


def gh_edit(number, title, body):
    with tempfile.NamedTemporaryFile("w", suffix=".md", delete=False) as f:
        f.write(body)
        path = f.name
    try:
        cmd = ["gh", "issue", "edit", str(number), "-R", REPO, "--title", title, "--body-file", path]
        subprocess.run(cmd, check=True, capture_output=True, text=True)
    finally:
        os.unlink(path)


def norm(text):
    return "\n".join(line.rstrip() for line in text.replace("\r\n", "\n").strip().split("\n"))


def find_differences(rendered, view=None):
    """Return a list of dicts for each row whose issue is missing or differs."""
    view = view or gh_view
    found = []
    for row_id, (title, body, number) in rendered.items():
        if number is None:
            found.append({"row": row_id, "number": None, "why": "no issue mapped in plan section 10"})
            continue
        live = view(number)
        why = []
        if norm(live["title"]) != norm(title):
            why.append("title")
        if norm(live["body"]) != norm(body):
            why.append("body")
        if why:
            found.append({"row": row_id, "number": number, "why": " and ".join(why),
                          "title": title, "body": body, "live": live})
    return found


def show_diff(d):
    live = d["live"]
    if norm(live["title"]) != norm(d["title"]):
        print(f"  title: {live['title']!r} -> {d['title']!r}")
    diff = difflib.unified_diff(norm(live["body"]).split("\n"), norm(d["body"]).split("\n"),
                                "github", "plan", lineterm="", n=1)
    for line in diff:
        print("  " + line)


def main(argv=None):
    """Run the tool; a missing gh, a failing gh or a changed plan layout exits 2 with a plain message."""
    try:
        return run(argv)
    except FileNotFoundError as e:
        if e.filename == "gh":
            print("plan-issues: the GitHub CLI `gh` was not found; install it and run `gh auth login`", file=sys.stderr)
        else:
            print(f"plan-issues: file not found: {e.filename or e}", file=sys.stderr)
    except subprocess.CalledProcessError as e:
        detail = (e.stderr or "").strip() or "no error text"
        print(f"plan-issues: `{' '.join(map(str, e.cmd[:3]))}` failed (exit {e.returncode}): {detail}", file=sys.stderr)
    except ValueError as e:
        print(f"plan-issues: cannot read the plan: {e}", file=sys.stderr)
    return 2


def run(argv):
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    mode = ap.add_mutually_exclusive_group(required=True)
    mode.add_argument("--print", dest="row", metavar="ROW", help="print the issue body of one row")
    mode.add_argument("--check", action="store_true", help="list issues that differ from the plan; exit 1 if any")
    mode.add_argument("--apply", action="store_true", help="edit the differing issues (writes to GitHub)")
    ap.add_argument("--diff", action="store_true", help="with --check, show each difference")
    ap.add_argument("--plan", default=str(PLAN), help="plan file (default: the repo's plan)")
    args = ap.parse_args(argv)

    rendered, rows = render_all(args.plan)
    if args.row:
        if args.row not in rendered:
            print(f"unknown row {args.row!r}; rows: {', '.join(rows)}", file=sys.stderr)
            return 2
        sys.stdout.write(rendered[args.row][1])
        return 0

    diffs = find_differences(rendered)
    for d in diffs:
        where = f"#{d['number']}" if d["number"] else "(none)"
        print(f"row {d['row']} {where}: {d['why']} differs" if d["number"] else f"row {d['row']}: {d['why']}")
        if args.diff and d["number"]:
            show_diff(d)
    print(f"{len(diffs)} of {len(rendered)} rows differ")
    if args.apply:
        for d in diffs:
            if d["number"]:
                gh_edit(d["number"], d["title"], d["body"])
                print(f"updated #{d['number']} (row {d['row']})")
        return 0
    return 1 if diffs else 0


if __name__ == "__main__":
    sys.exit(main())
