#!/usr/bin/env python3
"""Tests for scripts/plan-issues.py. No network: the gh calls are replaced."""

import importlib.util
import io
import tempfile
import unittest
from contextlib import redirect_stdout
from pathlib import Path
from unittest import mock

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location("plan_issues", ROOT / "scripts/plan-issues.py")
pi = importlib.util.module_from_spec(spec)
spec.loader.exec_module(pi)

FIXTURE = """# Plan

## 3. Prep

| # | Item | Why |
|---|---|---|
| 0.1 | Fixtures | because |
| 0.2 | The App | because |
| 0.3 | Release | because |

## 4. Layout

## 5. The PR sequence

| PR | Scope | Depends on | Acceptance test | Role | Skills |
|---|---|---|---|---|---|
| 1 | **Scaffolding:** `go.mod` and a `a|b` pipe in ticks | 0.3 | CI is green | develop-fleet | fleet-go |
| 2 | **Store (epic):** the store, see [02h](../specs/02h-x.md#a) | 1, 0.1 | tests pass, see [AGENTS.md](../../AGENTS.md) | develop-fleet-deep | fleet-sqlite-store, fleet-go |
| 2b | **`fleetd` shell:** the shell | 2 | it starts | develop-fleet | fleet-go |
| 3 | **Three:** third | 1 to 2, 2b, 9z, 0.9 | ok | develop-fleet (a), review-fleet-deep (b) | fleet-go |
| 4 | **Four:** fourth | 3 | ok | develop-fleet | fleet-go |

## 6. How it is tested

## 10. Tracking

| Plan ids | Issues |
|---|---|
| 1 to 4 (the numbered rows, in order) | #30 to #33 |
| 2b | #56 |
| 0.1 to 0.3 | #25 to #27 |

**Who merges.** text
"""


def load():
    return pi.parse_plan(FIXTURE)


class RenderTests(unittest.TestCase):
    def test_row_renders_expected_body(self):
        rows, prep, issue_of = load()
        title, body = pi.render_row("1", rows, prep, issue_of)
        self.assertEqual(title, "v1 PR 1: Scaffolding")
        self.assertEqual(body, f"""PR 1 of the [v1 implementation plan]({pi.PLAN_URL}).

## Scope
Scaffolding: `go.mod` and a `a|b` pipe in ticks

## Depends on
#27 (0.3)

## Acceptance test
CI is green

## Role and skills
- Role: `develop-fleet`
- Skills: fleet-go

## Done when
The [Pre-Ready Checklist]({pi.CHECKLIST_URL}) in AGENTS.md is met, and the owner has merged the PR.
""")

    def test_epic_title_keeps_its_parenthetical(self):
        rows, prep, issue_of = load()
        self.assertEqual(pi.render_row("2", rows, prep, issue_of)[0], "v1 PR 2: Store (epic)")
        self.assertEqual(pi.render_row("2b", rows, prep, issue_of)[0], "v1 PR 2b: `fleetd` shell")

    def test_mapping_covers_rows_and_prep(self):
        _, _, issue_of = load()
        self.assertEqual(issue_of["1"], 30)
        self.assertEqual(issue_of["4"], 33)
        self.assertEqual(issue_of["2b"], 56)
        self.assertEqual(issue_of["0.3"], 27)

    def test_range_dependency_expands(self):
        rows, prep, issue_of = load()
        deps = pi.render_deps("1 to 2, 2b", rows, prep, issue_of)
        self.assertEqual(deps, "#30, #31, #56")

    def test_prep_dependency_shows_its_id(self):
        rows, prep, issue_of = load()
        self.assertEqual(pi.render_deps("1, 0.1", rows, prep, issue_of), "#30, #25 (0.1)")

    def test_unknown_dependency_is_kept_as_text(self):
        rows, prep, issue_of = load()
        self.assertEqual(pi.render_deps("2b, 9z, 0.9", rows, prep, issue_of), "#56, 9z, 0.9")

    def test_all_above_is_plain_text(self):
        rows, prep, issue_of = load()
        self.assertEqual(pi.render_deps("all above, 0.3", rows, prep, issue_of),
                         "all rows above it in the plan table, #27 (0.3)")

    def test_relative_links_become_absolute(self):
        rows, prep, issue_of = load()
        _, body = pi.render_row("2", rows, prep, issue_of)
        self.assertIn("](https://github.com/linuxdynasty/fleet-board/blob/main/docs/specs/02h-x.md#a)", body)
        self.assertIn("](https://github.com/linuxdynasty/fleet-board/blob/main/AGENTS.md)", body)
        self.assertNotIn("](..", body)
        self.assertEqual(pi.absolute_links("[x](https://e.com/a) [y](#z)"), "[x](https://e.com/a) [y](#z)")

    def test_roles_are_backticked_skills_are_not(self):
        rows, prep, issue_of = load()
        _, body = pi.render_row("3", rows, prep, issue_of)
        self.assertIn("- Role: `develop-fleet` (a), `review-fleet-deep` (b)", body)

    def test_mapping_count_mismatch_is_an_error(self):
        bad = FIXTURE.replace("| 2b | #56 |", "| 2b | #56 to #57 |")
        with self.assertRaises(SystemExit):
            pi.parse_plan(bad)


class CheckTests(unittest.TestCase):
    def fixture_plan(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        plan = Path(tmp.name, "plan.md")
        plan.write_text(FIXTURE)
        return plan

    def rendered(self):
        rows, prep, issue_of = load()
        return {r: pi.render_row(r, rows, prep, issue_of) + (issue_of.get(r),) for r in rows}

    def test_identical_issues_do_not_differ(self):
        rendered = self.rendered()
        by_number = {n: {"title": t, "body": b + "\n"} for t, b, n in rendered.values()}
        self.assertEqual(pi.find_differences(rendered, view=lambda n: by_number[n]), [])

    def test_body_and_title_differences_are_listed(self):
        rendered = self.rendered()
        by_number = {n: {"title": t, "body": b} for t, b, n in rendered.values()}
        by_number[30] = {"title": by_number[30]["title"], "body": "old body"}
        by_number[31] = {"title": "v1 PR 2: old", "body": by_number[31]["body"]}
        found = pi.find_differences(rendered, view=lambda n: by_number[n])
        self.assertEqual([(d["row"], d["why"]) for d in found], [("1", "body"), ("2", "title")])

    def test_row_without_an_issue_is_reported(self):
        rendered = self.rendered()
        rendered["3"] = rendered["3"][:2] + (None,)
        by_number = {n: {"title": t, "body": b} for t, b, n in rendered.values() if n}
        found = pi.find_differences(rendered, view=lambda n: by_number[n])
        self.assertEqual([(d["row"], d["number"]) for d in found], [("3", None)])

    def test_check_exit_code_follows_differences(self):
        plan = self.fixture_plan()
        with mock.patch.object(pi, "gh_view", return_value={"title": "x", "body": "y"}), \
                redirect_stdout(io.StringIO()) as out:
            code = pi.main(["--check", "--plan", str(plan)])
        self.assertEqual(code, 1)
        self.assertIn("5 of 5 rows differ", out.getvalue())

    def test_apply_edits_only_differing_issues(self):
        rendered = self.rendered()
        by_number = {n: {"title": t, "body": b} for t, b, n in rendered.values()}
        by_number[33] = {"title": "t", "body": "b"}
        plan = self.fixture_plan()
        with mock.patch.object(pi, "gh_view", side_effect=lambda n: by_number[n]), \
                mock.patch.object(pi, "gh_edit") as edit, redirect_stdout(io.StringIO()):
            code = pi.main(["--apply", "--plan", str(plan)])
        self.assertEqual(code, 0)
        edit.assert_called_once()
        self.assertEqual(edit.call_args.args[0], 33)


class RealPlanTests(unittest.TestCase):
    def test_every_real_row_has_an_issue_and_renders(self):
        rows, prep, issue_of = pi.parse_plan(pi.PLAN.read_text())
        self.assertGreater(len(rows), 25)
        for r in rows:
            self.assertIn(r, issue_of, f"row {r} has no issue in plan section 10")
            title, body = pi.render_row(r, rows, prep, issue_of)
            self.assertTrue(title.startswith(f"v1 PR {r}: "))
            self.assertNotIn("](..", body)
        self.assertEqual(sorted(set(issue_of.values())), sorted(issue_of.values()), "an issue is mapped twice")


if __name__ == "__main__":
    unittest.main()
