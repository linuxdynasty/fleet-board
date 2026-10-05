#!/usr/bin/env python3
"""Seeded RED/GREEN cases for scripts/check-doc-names.py.

Each case writes docs and an allowlist into a throwaway tree and runs the real
script against an image of the fixture contract built once with the pinned buf
(proto_image_testkit.py). Exit codes: 0 clean, 1 unknown names or a bad
allowlist entry, 2 the contract could not be loaded (fail closed).
"""
from __future__ import annotations

import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from proto_image_testkit import ROOT, FixtureTree, clean_env  # noqa: E402

CHECK = ROOT / "scripts/check-doc-names.py"
ALLOW = ".agents/doc-names-allow.json"


def allow(*entries):
    return {"version": 1, "entries": [{"token": t, "kind": "test", "reason": "seeded"} for t in entries]}


class DocNameTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.images = tempfile.TemporaryDirectory()
        builder = FixtureTree()
        try:
            cls.image = builder.build_image(Path(cls.images.name) / "image.json")
        finally:
            builder.cleanup()

    @classmethod
    def tearDownClass(cls):
        cls.images.cleanup()

    def setUp(self):
        self.tree = FixtureTree()
        self.addCleanup(self.tree.cleanup)
        self.tree.write_json(ALLOW, allow())

    def run_check(self, image=True, **env):
        extra = {"FLEET_PROTO_IMAGE": str(self.image)} if image else {}
        extra.update(env)
        return subprocess.run([sys.executable, str(CHECK), "--root", str(self.tree.root)],
                              env=clean_env(**extra), capture_output=True, text=True, encoding="utf-8")

    def assert_rc(self, want, out):
        self.assertEqual(want, out.returncode, "stdout:\n%s\nstderr:\n%s" % (out.stdout, out.stderr))

    def test_seeded_unknown_name_fails_then_passes_once_removed(self):
        self.tree.write("docs/specs/a.md", "# A\n\nThe agent sends `LogChunk`.\nIt sends `NoSuchThing` too.\n")
        out = self.run_check()
        self.assert_rc(1, out)
        self.assertIn("docs/specs/a.md:4: `NoSuchThing`", out.stdout)
        self.assertNotIn("LogChunk", out.stdout)
        self.tree.write("docs/specs/a.md", "# A\n\nThe agent sends `LogChunk`.\n")
        self.assert_rc(0, self.run_check())

    def test_every_kind_of_name_is_known(self):
        self.tree.write("docs/plan/p.md", "`LogChunk`, `Marker`, `LogService`, `ReadLog`, `body`, `LogStream`.\n")
        self.assert_rc(0, self.run_check())

    def test_enum_full_value_and_short_form_pass(self):
        self.tree.write("docs/design.md", "`DELIVERY_CLASS_NEVER_DROP`, `NEVER_DROP`, `TERMINAL`, "
                                          "`DeliveryClass.NEVER_DROP`, `LogStream.LOG_STREAM_TERMINAL`.\n")
        self.assert_rc(0, self.run_check())

    def test_unknown_screaming_token_fails_including_after_a_pipe(self):
        self.tree.write("docs/features.md", "Delivery is `NEVER_DROP|SOMETIMES_DROP`.\n")
        out = self.run_check()
        self.assert_rc(1, out)
        self.assertIn("docs/features.md:1: `SOMETIMES_DROP`", out.stdout)
        self.assertNotIn("`NEVER_DROP`", out.stdout)

    def test_bad_enum_value_after_a_dot_fails(self):
        self.tree.write("docs/specs/e.md", "`DeliveryClass.SOMETIMES`\n")
        out = self.run_check()
        self.assert_rc(1, out)
        self.assertIn("`DeliveryClass.SOMETIMES`", out.stdout)

    def test_field_path_resolves_hop_by_hop(self):
        self.tree.write("docs/specs/p.md", "`LogChunk.key.stream`, `LogChunk.labels[]`, `LogChunk.labels[].name`, "
                                           "`LogChunk.marker.note`, `LogChunk.Marker.note`, `LogService.ReadLog`, "
                                           "`ReadLogResponse.chunk.key.session_id`, `fleet.fixture.v1.LogChunk.data`.\n")
        self.assert_rc(0, self.run_check())

    def test_a_bad_hop_fails_and_names_where_it_broke(self):
        self.tree.write("docs/specs/p.md", "ok `LogChunk.key.stream`\nbad `LogChunk.key.nope`\n")
        out = self.run_check()
        self.assert_rc(1, out)
        self.assertIn("docs/specs/p.md:2: `LogChunk.key.nope`", out.stdout)
        self.assertIn("LogStreamKey", out.stdout)

    def test_brackets_on_a_field_that_is_not_repeated_fail(self):
        self.tree.write("docs/specs/p.md", "`LogChunk.key[]`\n")
        self.assert_rc(1, self.run_check())

    def test_a_hop_past_a_scalar_fails(self):
        self.tree.write("docs/specs/p.md", "`LogChunk.data.size`\n")
        self.assert_rc(1, self.run_check())

    def test_fenced_code_is_ignored(self):
        self.tree.write("docs/specs/f.md", "```text\n`NoSuchThing`\n```\n~~~\n`AlsoMissing`\n~~~\nafter `LogChunk`\n")
        self.assert_rc(0, self.run_check())

    def test_a_code_fence_that_never_closes_fails(self):
        self.tree.write("docs/specs/f.md", "ok `LogChunk`\n\n```text\n`NoSuchThing`\n")
        out = self.run_check()
        self.assert_rc(1, out)
        self.assertIn("docs/specs/f.md:3: a code fence that never closes", out.stdout)

    def test_a_fleet_token_in_no_known_package_fails(self):
        self.tree.write("docs/specs/p.md", "`fleet.fixture.v2.LogChunk` and `fleet.nope`\n")
        out = self.run_check()
        self.assert_rc(1, out)
        self.assertIn("`fleet.fixture.v2.LogChunk` (no fleet package matches)", out.stdout)
        self.assertIn("`fleet.nope` (no fleet package matches)", out.stdout)

    def test_excluded_folders_are_ignored(self):
        for rel in ("docs/reviews/r.md", "docs/research/r.md", "docs/adr/0001-old.md", "README.md", "docs/other.md"):
            self.tree.write(rel, "`NoSuchThing`\n")
        self.assert_rc(0, self.run_check())

    def test_the_adr_index_is_checked(self):
        self.tree.write("docs/adr/README.md", "`NoSuchThing`\n")
        self.assert_rc(1, self.run_check())

    def test_syntactic_exclusions(self):
        self.tree.write("docs/specs/s.md", "`Missing.md`, `events.proto`, `docs/specs/README.md`, "
                                           "`api/fleet/fixture/v1/fixture.proto`, `~/.config/fleet/policy.toml`, "
                                           "`https://example.com/Nope/Path`, `www.Example.com/Nope`, "
                                           "`X-Forwarded-For`, `PROJ-123`, `tmux -L fleet`, `<NAME>`, `<reason>`, "
                                           "`<device id>`, `TRIAGE_KIND_<NAME>`, `<ENUM>_<NAME>`, `<Rpc>Request`, "
                                           "`/signin?error=<REASON>`, `A`, `some_field`, `os.Getenv`, "
                                           "`create_time > \"2026-09-25T00:00:00Z\"`.\n")
        self.assert_rc(0, self.run_check())

    def test_names_around_slashes_arrows_and_angle_brackets_are_checked(self):
        bad = ("ReadLog->FooBarTwo", "ReadLog/FooBarThree", "<FooBarFour>", "map<string, FooBarFive>",
               "FooBarSix{/goal …}", "GET /fleet.fixture.v1.LogService/NoSuchRpc", "api/Missing",
               "TERMINAL/SOMETIMES_DROP")
        self.tree.write("docs/specs/s.md", "".join("`%s`\n" % span for span in bad))
        self.tree.write_json(ALLOW, allow("GET"))
        out = self.run_check()
        self.assert_rc(1, out)
        for line, token in enumerate(("FooBarTwo", "FooBarThree", "FooBarFour", "FooBarFive", "FooBarSix",
                                      "NoSuchRpc", "Missing", "SOMETIMES_DROP"), 1):
            with self.subTest(token=token):
                self.assertIn("docs/specs/s.md:%d: `%s`" % (line, token), out.stdout)
        for real in ("`ReadLog`", "`LogService`", "`fleet.fixture.v1.LogService`", "`TERMINAL`"):
            self.assertNotIn(real, out.stdout)

    def test_real_names_around_slashes_arrows_and_angle_brackets_pass(self):
        self.tree.write("docs/specs/s.md", "`ReadLog->LogChunk`, `LogService/ReadLog`, `<LogChunk>`, "
                                           "`map<string, Label>`, `LogChunk{/x …}`, `TERMINAL/NEVER_DROP`, "
                                           "`GET /fleet.fixture.v1.LogService/ReadLog`.\n")
        self.tree.write_json(ALLOW, allow("GET"))
        self.assert_rc(0, self.run_check())

    def test_a_mixed_case_name_with_an_underscore_is_checked(self):
        self.tree.write("docs/specs/m.md", "`FooBar_Mixed` and `Log_Chunk.key`\n")
        out = self.run_check()
        self.assert_rc(1, out)
        self.assertIn("`FooBar_Mixed`", out.stdout)
        self.assertIn("`Log_Chunk.key`", out.stdout)

    def test_well_known_types_are_not_fleet_names(self):
        self.tree.write("docs/specs/w.md", "`Timestamp`\n")
        self.assert_rc(1, self.run_check())

    def test_an_allowlisted_token_passes(self):
        self.tree.write("docs/development/h.md", "The hook `PostToolUse` fires.\n")
        self.tree.write_json(ALLOW, allow("PostToolUse"))
        self.assert_rc(0, self.run_check())

    def test_an_allowlist_entry_no_doc_uses_fails(self):
        self.tree.write("docs/specs/a.md", "`LogChunk`\n")
        self.tree.write_json(ALLOW, allow("PostToolUse"))
        out = self.run_check()
        self.assert_rc(1, out)
        self.assertIn("PostToolUse", out.stdout)
        self.assertIn("no checked doc uses it", out.stdout)

    def test_an_allowlist_entry_that_is_a_real_proto_name_fails(self):
        self.tree.write("docs/specs/a.md", "`LogChunk` and `NEVER_DROP`\n")
        self.tree.write_json(ALLOW, allow("LogChunk"))
        out = self.run_check()
        self.assert_rc(1, out)
        self.assertIn("is a real proto name", out.stdout)

    def test_a_file_limited_entry_passes_in_its_file_and_fails_in_another(self):
        entry = {"token": "SelectPane", "kind": "retired", "reason": "seeded", "file": "docs/specs/03g.md"}
        self.tree.write_json(ALLOW, {"version": 1, "entries": [entry]})
        self.tree.write("docs/specs/03g.md", "Retired: `SelectPane`.\n")
        self.assert_rc(0, self.run_check())
        self.tree.write("docs/specs/02.md", "\nStill uses `SelectPane`.\n")
        out = self.run_check()
        self.assert_rc(1, out)
        self.assertIn("docs/specs/02.md:2: `SelectPane`", out.stdout)
        self.assertNotIn("03g.md:", out.stdout)

    def test_a_file_limited_entry_its_file_does_not_use_fails(self):
        entry = {"token": "SelectPane", "kind": "retired", "reason": "seeded", "file": "docs/specs/03g.md"}
        self.tree.write_json(ALLOW, {"version": 1, "entries": [entry]})
        self.tree.write("docs/specs/03g.md", "Nothing retired here.\n")
        self.tree.write("docs/specs/02.md", "`SelectPane`\n")
        out = self.run_check()
        self.assert_rc(1, out)
        self.assertIn("`SelectPane`: docs/specs/03g.md does not use it", out.stdout)
        self.assertIn("docs/specs/02.md:1: `SelectPane`", out.stdout)

    def test_a_file_limited_entry_that_is_a_real_proto_name_fails(self):
        entry = {"token": "LogChunk", "kind": "retired", "reason": "seeded", "file": "docs/specs/a.md"}
        self.tree.write_json(ALLOW, {"version": 1, "entries": [entry]})
        self.tree.write("docs/specs/a.md", "`LogChunk`\n")
        out = self.run_check()
        self.assert_rc(1, out)
        self.assertIn("is a real proto name", out.stdout)

    def test_an_allowlist_file_that_is_not_a_string_fails_closed(self):
        entry = {"token": "SelectPane", "kind": "retired", "reason": "seeded", "file": 3}
        self.tree.write_json(ALLOW, {"version": 1, "entries": [entry]})
        self.assert_rc(2, self.run_check())

    def test_an_allowlist_entry_without_a_reason_fails_closed(self):
        self.tree.write("docs/specs/a.md", "`PostToolUse`\n")
        self.tree.write_json(ALLOW, {"version": 1, "entries": [{"token": "PostToolUse", "kind": "hook", "reason": " "}]})
        self.assert_rc(2, self.run_check())

    def test_a_missing_allowlist_fails_closed(self):
        os.remove(self.tree.root / ALLOW)
        self.assert_rc(2, self.run_check())

    def test_a_missing_image_fails_closed(self):
        self.tree.write("docs/specs/a.md", "`LogChunk`\n")
        out = self.run_check(FLEET_PROTO_IMAGE=str(self.tree.root / "no-such-image.json"))
        self.assert_rc(2, out)

    def test_a_broken_image_fails_closed(self):
        broken = self.tree.write("broken.json", "{not json")
        self.assert_rc(2, self.run_check(FLEET_PROTO_IMAGE=str(broken)))

    def test_an_unresolvable_type_reference_fails_closed(self):
        bad = {"file": [{"name": "fleet/x/v1/x.proto", "package": "fleet.x.v1", "messageType": [
            {"name": "Holder", "field": [{"name": "gone", "number": 1, "label": "LABEL_OPTIONAL",
                                          "type": "TYPE_MESSAGE", "typeName": ".fleet.x.v1.Missing"}]}]}]}
        path = self.tree.write_json("bad-image.json", bad)
        self.assert_rc(2, self.run_check(FLEET_PROTO_IMAGE=str(path)))

    def test_builds_the_contract_with_the_pinned_buf_when_no_image_is_given(self):
        self.tree.write("docs/specs/a.md", "`LogChunk.key.stream`\n")
        self.assert_rc(0, self.run_check(image=False))

    def test_a_contract_that_does_not_compile_fails_closed(self):
        self.tree.write("docs/specs/a.md", "`LogChunk`\n")
        self.tree.proto.write_text(self.tree.proto.read_text(encoding="utf-8") + "\nmessage Broken {\n",
                                   encoding="utf-8")
        self.assert_rc(2, self.run_check(image=False))

    def test_missing_go_fails_closed(self):
        self.tree.write("docs/specs/a.md", "`LogChunk`\n")
        self.assert_rc(2, self.run_check(image=False, PATH=str(self.tree.root / "no-bin")))


if __name__ == "__main__":
    unittest.main()
