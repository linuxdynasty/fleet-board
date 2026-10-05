#!/usr/bin/env python3
"""Seeded RED/GREEN cases for scripts/check-docs-stale.py.

Each case builds a throwaway git repo with its own .agents/doc-paths.json and
specs, makes commits, and runs the real script: --ci --range (0 clean, 1
findings, 2 internal error), hook mode (always 0) and --check-map.
"""
from __future__ import annotations

import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
CHECK = ROOT / "scripts/check-docs-stale.py"
SPECS = ("docs/specs/02h.md", "docs/specs/01.md")
MAP = {"version": 1, "rules": [
    {"globs": ["internal/agent/tmux/**"], "specs": list(SPECS), "exclude": ["*.md"], "note": "test rule"},
    {"globs": ["internal/not/yet/**"], "specs": ["docs/specs/01.md"]},
]}


class DocsStaleTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.repo = Path(self.tmp.name) / "repo"
        self.env = {"PATH": "/usr/bin:/bin:/usr/local/bin:/opt/homebrew/bin", "HOME": self.tmp.name,
                    "GIT_CONFIG_GLOBAL": "/dev/null", "GIT_CONFIG_SYSTEM": "/dev/null"}
        self.git("init", "-q", "-b", "main", str(self.repo), cwd=self.tmp.name)
        self.git("config", "user.email", "t@example.com")
        self.git("config", "user.name", "t")
        self.write(".agents/doc-paths.json", json.dumps(MAP))
        for spec in SPECS:
            self.write(spec, "# spec\n")
        self.commit("base")
        self.base = self.git("rev-parse", "HEAD").strip()

    def git(self, *args, cwd=None):
        out = subprocess.run(["git", *args], cwd=cwd or self.repo, env=self.env, capture_output=True, text=True, encoding="utf-8")
        self.assertEqual(0, out.returncode, out.stderr)
        return out.stdout

    def write(self, rel, text):
        path = self.repo / rel
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text, encoding="utf-8")

    def commit(self, message):
        self.git("add", "-A")
        self.git("commit", "-q", "-m", message)

    def go_package(self, rel_dir, docs=("doc.go", "README.md", "AGENTS.md")):
        self.write(rel_dir + "/x.go", "package x\n")
        for name in docs:
            self.write(rel_dir + "/" + name, "package x\n" if name == "doc.go" else "# x\n")

    def run_check(self, *args):
        return subprocess.run([sys.executable, str(CHECK), "--root", str(self.repo), *args],
                              env=self.env, capture_output=True, text=True, encoding="utf-8")

    def ci(self, rng=None):
        return self.run_check("--ci", "--range", rng or self.base + "..HEAD")

    def assert_rc(self, want, out):
        self.assertEqual(want, out.returncode, "stdout:\n%s\nstderr:\n%s" % (out.stdout, out.stderr))

    def test_mapped_code_change_without_a_spec_change_fails(self):
        self.go_package("internal/agent/tmux")
        self.commit("tmux: observer")
        out = self.ci()
        self.assert_rc(1, out)
        self.assertIn("internal/agent/tmux/x.go", out.stdout)
        self.assertIn("docs/specs/02h.md", out.stdout)
        self.assertIn("No-Doc-Change:", out.stdout)

    def test_the_trailer_with_a_reason_passes(self):
        self.go_package("internal/agent/tmux")
        self.commit("tmux: observer\n\nNo-Doc-Change: renames a local variable; 02h is unchanged")
        self.assert_rc(0, self.ci())

    def test_a_placeholder_reason_fails(self):
        for n, reason in enumerate(("TODO", "tbd", "n/a", "none", "-", "", "TODO: later", "<reason>",
                                    "<anything>", "reason", "Reason", "\u2014", "\u2013")):
            with self.subTest(reason=reason):
                self.go_package("internal/agent/tmux/p%d" % n)
                self.commit("tmux\n\nNo-Doc-Change: " + reason)
                out = self.ci()
                self.assert_rc(1, out)
                self.assertIn("placeholder", out.stdout)

    def test_a_trailer_outside_the_range_does_not_count(self):
        self.write("notes.txt", "x\n")
        self.commit("before\n\nNo-Doc-Change: an older commit's reason")
        start = self.git("rev-parse", "HEAD").strip()
        self.go_package("internal/agent/tmux")
        self.commit("tmux: observer")
        self.assert_rc(1, self.ci(start + "..HEAD"))

    def test_changing_any_one_mapped_spec_passes(self):
        self.go_package("internal/agent/tmux")
        self.write("docs/specs/01.md", "# spec\n\nE2 now says more.\n")
        self.commit("tmux: observer, with 01 E2")
        self.assert_rc(0, self.ci())

    def test_a_markdown_change_in_a_mapped_folder_is_excluded(self):
        self.write("internal/agent/tmux/notes.md", "# notes\n")
        self.commit("notes")
        self.assert_rc(0, self.ci())

    def test_an_unmapped_change_passes(self):
        self.write("tools/thing.txt", "x\n")
        self.commit("tools")
        self.assert_rc(0, self.ci())

    def test_a_changed_go_package_missing_agents_md_fails(self):
        self.go_package("internal/foo", docs=("doc.go", "README.md"))
        self.commit("foo")
        out = self.ci()
        self.assert_rc(1, out)
        self.assertIn("internal/foo: missing AGENTS.md", out.stdout)

    def test_a_package_with_a_non_ascii_path_is_still_checked(self):
        self.go_package("internal/caf\u00e9", docs=("doc.go", "README.md"))
        self.commit("cafe")
        out = self.ci()
        self.assert_rc(1, out)
        self.assertIn("internal/caf\u00e9: missing AGENTS.md", out.stdout)

    def real_map(self):
        """Commit the repo's own map with a stub for every spec it names; return that commit."""
        real = json.loads((ROOT / ".agents/doc-paths.json").read_text(encoding="utf-8"))
        self.write(".agents/doc-paths.json", json.dumps(real))
        for rule in real["rules"]:
            for spec in rule["specs"]:
                self.write(spec, "# spec\n")
        self.commit("real map")
        return self.git("rev-parse", "HEAD").strip()

    def test_a_new_package_in_a_folder_a_spec_defines_needs_that_spec(self):
        base = self.real_map()
        for folder, spec in (("internal/agent/policy", "docs/specs/03e-settings-and-drift.md"),
                             ("internal/agent/goals", "docs/specs/01f-agent-control-goals-and-policy.md"),
                             ("internal/harness/claude", "docs/specs/01b-harness-commands.md"),
                             ("internal/hub/gateway", "docs/specs/02i-delivery-and-sequencing.md"),
                             ("internal/store", "docs/specs/03f-hub-internals.md")):
            with self.subTest(folder=folder):
                self.git("reset", "-q", "--hard", base)
                self.go_package(folder)
                self.commit(folder)
                out = self.ci(base + "..HEAD")
                self.assert_rc(1, out)
                self.assertIn(folder + "/x.go", out.stdout)
                self.assertIn(spec, out.stdout)
                self.write(spec, "# spec\n\nchanged\n")
                self.commit(folder + ", with its spec")
                self.assert_rc(0, self.ci(base + "..HEAD"))

    def test_a_new_api_package_needs_02_or_02e_under_the_real_map(self):
        base = self.real_map()
        self.write("api/fleet/newpkg/v1/x.proto", "syntax = \"proto3\";\n")
        self.commit("newpkg")
        out = self.ci(base + "..HEAD")
        self.assert_rc(1, out)
        self.assertIn("api/fleet/newpkg/v1/x.proto", out.stdout)
        self.assertIn("docs/specs/02e-contract-rules.md", out.stdout)
        self.git("reset", "-q", "--hard", base)
        self.write("api/fleet/hub/v1/x.proto", "syntax = \"proto3\";\n")
        self.write("docs/specs/02b-hub-api.md", "# spec\n\nchanged\n")
        self.commit("hub, with 02b")
        self.assert_rc(0, self.ci(base + "..HEAD"))

    def test_the_trailer_does_not_excuse_missing_package_docs(self):
        self.go_package("internal/foo", docs=("doc.go", "README.md"))
        self.commit("foo\n\nNo-Doc-Change: a new package with no spec yet")
        self.assert_rc(1, self.ci())

    def test_changes_main_made_after_the_branch_point_are_not_the_branchs(self):
        self.git("checkout", "-q", "-b", "pr")
        self.go_package("internal/agent/tmux")
        self.write("docs/specs/02h.md", "# spec\n\nobserver\n")
        self.commit("tmux, with 02h")
        self.git("checkout", "-q", "main")
        self.write("internal/not/yet/notes.txt", "x\n")
        self.commit("main moves on\n\nNo-Doc-Change: main's own reason, not the branch's")
        self.assert_rc(0, self.ci("main..pr"))

    def test_an_internal_error_exits_2(self):
        self.write("tools/thing.txt", "x\n")
        self.commit("caf\u00e9")
        self.git("config", "i18n.logOutputEncoding", "ISO-8859-1")
        out = self.ci()
        self.assert_rc(2, out)
        self.assertIn("internal error", out.stderr)

    def test_a_deleted_package_needs_no_docs(self):
        self.go_package("internal/foo")
        self.commit("foo")
        start = self.git("rev-parse", "HEAD").strip()
        self.git("rm", "-q", "-r", "internal/foo")
        self.commit("remove foo")
        self.assert_rc(0, self.ci(start + "..HEAD"))

    def test_an_empty_or_reversed_range_says_there_is_nothing_to_check(self):
        self.write("tools/thing.txt", "x\n")
        self.commit("tools")
        for rng in ("HEAD..HEAD", "HEAD.." + self.base):
            with self.subTest(rng=rng):
                out = self.ci(rng)
                self.assert_rc(0, out)
                self.assertIn("no commits in %s; nothing to check" % rng, out.stdout)
                self.assertNotIn("docs move with the code", out.stdout)

    def test_hook_mode_on_a_failing_tree_exits_zero_and_reminds(self):
        self.go_package("internal/agent/tmux", docs=("doc.go", "README.md"))
        out = self.run_check()
        self.assert_rc(0, out)
        self.assertIn("docs/specs/02h.md", out.stdout)
        self.assertIn("missing AGENTS.md", out.stdout)

    def test_hook_mode_with_a_broken_map_still_exits_zero(self):
        self.write(".agents/doc-paths.json", "{not json")
        self.go_package("internal/agent/tmux")
        self.assert_rc(0, self.run_check())

    def test_an_unresolvable_range_exits_2(self):
        for rng in ("no-such-ref..HEAD", "HEAD", "HEAD.."):
            with self.subTest(rng=rng):
                out = self.ci(rng)
                self.assert_rc(2, out)
                self.assertIn("cannot resolve the range", out.stderr)

    def test_a_failing_git_call_is_fatal_in_ci(self):
        plain = Path(self.tmp.name) / "plain"
        plain.mkdir()
        out = subprocess.run([sys.executable, str(CHECK), "--root", str(plain), "--ci", "--range", "a..b"],
                             env=self.env, capture_output=True, text=True, encoding="utf-8")
        self.assert_rc(2, out)
        self.assertIn("git", out.stderr)

    def test_a_broken_map_exits_2_in_ci(self):
        self.write(".agents/doc-paths.json", "{not json")
        self.commit("break the map")
        out = self.ci()
        self.assert_rc(2, out)
        self.assertIn("doc-paths.json", out.stderr)

    def test_check_map_passes_with_folders_that_do_not_exist_yet(self):
        self.assert_rc(0, self.run_check("--check-map"))

    def test_check_map_fails_on_a_spec_that_does_not_exist(self):
        bad = json.loads(json.dumps(MAP))
        bad["rules"][1]["specs"] = ["docs/specs/01.md", "docs/specs/99-gone.md"]
        self.write(".agents/doc-paths.json", json.dumps(bad))
        out = self.run_check("--check-map")
        self.assert_rc(1, out)
        self.assertIn("docs/specs/99-gone.md", out.stdout)

    def test_check_map_fails_on_a_rule_without_specs(self):
        bad = {"version": 1, "rules": [{"globs": ["internal/x/**"], "specs": []}]}
        self.write(".agents/doc-paths.json", json.dumps(bad))
        self.assert_rc(1, self.run_check("--check-map"))


if __name__ == "__main__":
    unittest.main()
