#!/usr/bin/env python3
"""Report documentation that an in-progress change has left behind.

Tool-neutral and read-only: the PostToolUse hooks for Claude Code, Codex and
Muse call it after an edit, and a developer can run it any time. It never
fails (exit 0); it prints one reminder per line, or nothing.

Usage: scripts/check-docs-stale.py [--root DIR]

What it checks, from `git status` (so it needs no payload and works for any
harness, including Codex whose apply_patch payload names several files):

1. Go package docs. For each directory with a changed non-test .go file, the
   package must carry doc.go, README.md and a scoped AGENTS.md (AGENTS.md
   "Documentation Discipline"), and none of them may be older than the newest
   changed source file in that directory. Dirs under spikes/, vendor/ and
   testdata/ are skipped.
2. Contract docs. When an api/**/*.proto file changed, the contract specs
   docs/specs/02-contracts.md, 02b-hub-api.md and ui-contract-map.md must
   change in the same PR. A spec already changed in this branch (committed or
   in the working tree) counts as done.
"""
from __future__ import annotations

import argparse
import subprocess
import sys
import time
from pathlib import Path

CONTRACT_DOCS = (
    "docs/specs/02-contracts.md",
    "docs/specs/02b-hub-api.md",
    "docs/specs/ui-contract-map.md",
)
GO_DOCS = ("doc.go", "README.md", "AGENTS.md")
SKIP_PARTS = {"spikes", "vendor", "testdata"}


def git(root, *args):
    result = subprocess.run(["git", "-C", str(root), *args], capture_output=True, text=True)
    return result.stdout if result.returncode == 0 else ""


def working_tree_changes(root):
    """Paths from `git status`, including untracked files and deletions."""
    fields = git(root, "status", "--porcelain=v1", "-z", "--untracked-files=all").split("\0")
    paths, i = [], 0
    while i < len(fields):
        entry = fields[i]
        i += 1
        if len(entry) < 4:
            continue
        paths.append(entry[3:])
        if entry[0] in "RC":  # the next field is the rename source
            i += 1
    return paths


def branch_changes(root):
    """Paths that differ from the merge base with the default branch."""
    for base in ("origin/main", "main"):
        merge_base = git(root, "merge-base", "HEAD", base).strip()
        if merge_base:
            return [p for p in git(root, "diff", "--name-only", merge_base, "HEAD").split("\n") if p]
    return []


def go_findings(root, changed):
    newest = {}
    for rel in changed:
        path = Path(rel)
        if path.suffix != ".go" or path.name == "doc.go" or path.name.endswith("_test.go"):
            continue
        if SKIP_PARTS & set(path.parts):
            continue
        full = root / rel
        mtime = full.stat().st_mtime if full.exists() else time.time()
        directory = str(path.parent)
        newest[directory] = max(newest.get(directory, 0), mtime)
    findings = []
    for directory, mtime in sorted(newest.items()):
        if not any(p.suffix == ".go" and not p.name.endswith("_test.go") for p in (root / directory).glob("*.go")):
            continue  # the package is gone
        for name in GO_DOCS:
            doc = root / directory / name
            if not doc.is_file():
                findings.append("%s: missing %s (every Go package carries doc.go, README.md and AGENTS.md)" % (directory, name))
            elif doc.stat().st_mtime < mtime:
                findings.append("%s: %s is older than the Go source changed here; update it or confirm it is still true" % (directory, name))
    return findings


def contract_findings(root, changed, in_branch):
    protos = sorted(p for p in changed if p.startswith("api/") and p.endswith(".proto"))
    if not protos:
        return []
    done = set(changed) | set(in_branch)
    todo = [d for d in CONTRACT_DOCS if d not in done]
    if not todo:
        return []
    return ["contract changed (%s): update %s in the same PR (AGENTS.md Documentation Discipline); not yet changed here"
            % (", ".join(protos[:3]) + (", ..." if len(protos) > 3 else ""), ", ".join(todo))]


def main():
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    parser.add_argument("--root", default=".")
    args = parser.parse_args()
    root = Path(git(args.root, "rev-parse", "--show-toplevel").strip() or args.root).resolve()
    changed = working_tree_changes(root)
    for line in go_findings(root, changed) + contract_findings(root, changed, branch_changes(root)):
        print(line)


if __name__ == "__main__":
    try:
        main()
    except Exception:  # a reminder tool must never break an edit
        pass
