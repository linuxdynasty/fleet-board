#!/usr/bin/env python3
"""List the v1 capabilities in 01 that no test names (plan row 1b, ruling F8).

A capability counts as tested when its id (A1, E9, ...) appears as a whole word
in a test file: a Go `*_test.go`, a `scripts/test-*` self-test, or a web test
(`*.test.*` or `*.spec.*` under web/). A whole word has no letter or digit on
either side, so `TestEnroll_A1` names A1 and `A10` does not. Testdata,
node_modules, gen/ and .git are skipped.

This is a report, not a gate, until plan row 17: findings exit 0. A broken
input (01's table missing or empty, a test file that cannot be read) exits 2.

Usage: python3 scripts/report-capability-tests.py [--root DIR]
"""

import argparse
import re
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from proto_tags import ContractError, capability_ids  # noqa: E402

SKIP_DIRS = {".git", "gen", "node_modules", "testdata", ".bin"}


def is_test(rel):
    name = rel.name
    if name.endswith("_test.go"):
        return True
    if rel.parts[:1] == ("scripts",) and len(rel.parts) == 2 and name.startswith("test-"):
        return True
    if rel.parts[:1] == ("web",) and (".test." in name or ".spec." in name):
        return True
    return False


def test_files(root):
    stack = [root]
    while stack:
        folder = stack.pop()
        for path in sorted(folder.iterdir()):
            if path.is_symlink():
                continue
            if path.is_dir():
                if path.name not in SKIP_DIRS:
                    stack.append(path)
            elif is_test(path.relative_to(root)):
                yield path


def report(root):
    ids = [cap for cap, phase in capability_ids(root) if phase == "v1"]
    words = set()
    for path in test_files(root):
        rel = path.relative_to(root)
        try:
            text = path.read_text(encoding="utf-8")
        except (OSError, UnicodeDecodeError) as err:
            raise ContractError("cannot read %s: %s" % (rel, err)) from err
        words.update(re.findall(r"(?<![A-Za-z0-9])[A-Z][0-9]+(?![A-Za-z0-9])", text))
    return ids, [cap for cap in ids if cap not in words]


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--root", default=str(Path(__file__).resolve().parent.parent))
    args = parser.parse_args(argv)
    try:
        ids, missing = report(Path(args.root))
    except ContractError as err:
        print("report-capability-tests: %s" % err, file=sys.stderr)
        return 2
    print("report-capability-tests: %d of %d v1 capabilities have no test naming them"
          " (a report until plan row 17)" % (len(missing), len(ids)))
    print("untested: " + " ".join(missing))
    return 0


if __name__ == "__main__":
    sys.exit(main())
