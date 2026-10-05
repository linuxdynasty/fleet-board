"""Shared setup for test-check-doc-names.py and test-proto-image.py.

FixtureTree is a throwaway tree with buf.yaml, tool-versions.env and the
fixture contract (scripts/testdata/doc-names/fixture.proto) at
api/fleet/fixture/v1/. build_image() runs the real loader's `build`, which runs
the pinned buf, so the tests read the same compiled image the gates do.
"""
from __future__ import annotations

import json
import os
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
LOADER = ROOT / "scripts/proto_image.py"
FIXTURE = ROOT / "scripts/testdata/doc-names/fixture.proto"
BUF_YAML = "version: v2\nmodules:\n  - path: api\n"


def clean_env(**extra):
    env = {k: v for k, v in os.environ.items() if not k.startswith(("GIT_", "FLEET_"))}
    env.update(extra)
    return env


class FixtureTree:
    def __init__(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name)
        (self.root / "buf.yaml").write_text(BUF_YAML)
        shutil.copy(ROOT / "tool-versions.env", self.root / "tool-versions.env")
        self.proto = self.root / "api/fleet/fixture/v1/fixture.proto"
        self.proto.parent.mkdir(parents=True)
        shutil.copy(FIXTURE, self.proto)

    def cleanup(self):
        self.tmp.cleanup()

    def build_image(self, out):
        result = subprocess.run(
            [sys.executable, str(LOADER), "build", "--root", str(self.root), "--out", str(out)],
            env=clean_env(), capture_output=True, text=True,
        )
        if result.returncode != 0:
            raise RuntimeError("fixture image build failed: " + result.stderr)
        return out

    def write(self, rel, text):
        path = self.root / rel
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text)
        return path

    def write_json(self, rel, data):
        return self.write(rel, json.dumps(data, indent=1) + "\n")
