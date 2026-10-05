#!/usr/bin/env python3
"""Contract cases for scripts/proto_image.py, the loader the doc-drift scripts share.

Builds the fixture contract once with the pinned buf and checks what later
generators rely on: elements by kind, comments, field types, rpc shapes, and
hop-by-hop resolution.
"""
from __future__ import annotations

import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import proto_image  # noqa: E402
from proto_image_testkit import FixtureTree  # noqa: E402


class LoaderTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.tmp = tempfile.TemporaryDirectory()
        tree = FixtureTree()
        try:
            image = tree.build_image(Path(cls.tmp.name) / "image.json")
        finally:
            tree.cleanup()
        cls.c = proto_image.load(Path(cls.tmp.name), str(image))

    @classmethod
    def tearDownClass(cls):
        cls.tmp.cleanup()

    def test_only_fleet_files_are_names(self):
        self.assertEqual({"fleet.fixture.v1"}, self.c.packages)
        self.assertIn("fleet.fixture.v1.LogChunk", self.c.messages)
        self.assertIn("fleet.fixture.v1.LogChunk.Marker", self.c.messages)
        self.assertNotIn("google.protobuf.Timestamp", self.c.messages)
        self.assertFalse(self.c.is_name("Timestamp"))

    def test_leading_comments_are_kept_per_element(self):
        self.assertIn("one piece of a session log", self.c.messages["fleet.fixture.v1.LogChunk"].comment)
        enum = self.c.enums["fleet.fixture.v1.DeliveryClass"]
        self.assertIn("Never dropped", enum.values["DELIVERY_CLASS_NEVER_DROP"].comment)
        rpc = self.c.services["fleet.fixture.v1.LogService"].rpcs["ReadLog"]
        self.assertIn("[READ]", rpc.comment)

    def test_rpc_shape(self):
        rpc = self.c.services["fleet.fixture.v1.LogService"].rpcs["ReadLog"]
        self.assertEqual("fleet.fixture.v1.ReadLogRequest", rpc.input)
        self.assertEqual("fleet.fixture.v1.ReadLogResponse", rpc.output)
        self.assertFalse(rpc.server_streaming)

    def test_fields_resolve_to_their_types(self):
        msg = self.c.messages["fleet.fixture.v1.LogChunk"]
        self.assertTrue(msg.fields["labels"].repeated)
        self.assertEqual("fleet.fixture.v1.Label", msg.fields["labels"].target.full_name)
        self.assertEqual("google.protobuf.Timestamp", msg.fields["sent_time"].target.full_name)
        self.assertEqual(["text", "marker"], list(msg.oneofs["body"].fields))
        self.assertEqual("bytes", msg.fields["data"].type)

    def test_enum_short_forms(self):
        enum = self.c.enums["fleet.fixture.v1.DeliveryClass"]
        self.assertEqual("DELIVERY_CLASS_", enum.prefix)
        self.assertIs(enum.value("NEVER_DROP"), enum.value("DELIVERY_CLASS_NEVER_DROP"))
        self.assertTrue(self.c.is_enum_value("NEVER_DROP"))

    def test_resolve(self):
        found, why = self.c.resolve("LogChunk.key.stream")
        self.assertIsNone(why)
        self.assertEqual("fleet.fixture.v1.LogStream", found.full_name)
        found, why = self.c.resolve("LogChunk.key.nope")
        self.assertIsNone(found)
        self.assertIn("LogStreamKey has no field", why)

    def test_upper_snake(self):
        for name, want in (("DeliveryClass", "DELIVERY_CLASS"), ("HTTPStatus", "HTTP_STATUS"), ("Os2Kind", "OS2_KIND")):
            self.assertEqual(want, proto_image.upper_snake(name))


if __name__ == "__main__":
    unittest.main()
