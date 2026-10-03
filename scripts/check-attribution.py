#!/usr/bin/env python3
"""Fail when a commit message or a PR body carries AI attribution.

Owner rule (AGENTS.md, Non-Negotiable Rules): no `Co-Authored-By` trailer for a
model, no "Generated with" line, no "drafted with". Older history keeps its
trailers, so only a range is checked (default `origin/main..HEAD`).

Usage:
  scripts/check-attribution.py [--root DIR] [--range A..B]
  scripts/check-attribution.py --stdin      # check a PR body read from stdin

Exit 0: clean (or no origin/main to compare with, which is said out loud).
Exit 1: attribution found; each hit is printed with the commit and the line.
"""
from __future__ import annotations

import argparse
import re
import subprocess
import sys

MODELS = r"(claude|anthropic|gpt|openai|chatgpt|codex|muse|copilot|gemini|opencode|cursor|llm|\bai\b)"
TOOLS = r"(claude|chatgpt|gpt|codex|copilot|gemini|cursor|opencode|muse|an? ai\b|ai\b)"
RULES = [
    re.compile(r"^\s*co-authored-by:.*" + MODELS, re.I),
    re.compile(r"generated\s+(with|by)\s+\W*" + TOOLS, re.I),
    re.compile(r"^\W*(drafted|written|authored)\s+(with|by)\s+\W*" + TOOLS, re.I),
    re.compile("\U0001F916"),  # the robot emoji used in "Generated with" footers
]


def hits(text):
    found = []
    for line in text.splitlines():
        if any(rule.search(line) for rule in RULES):
            found.append(line.strip())
    return found


def git(root, *args):
    result = subprocess.run(["git", "-C", root, *args], capture_output=True, text=True)
    return result.returncode, result.stdout


def main():
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    parser.add_argument("--root", default=".")
    parser.add_argument("--range", dest="rev_range", default="origin/main..HEAD")
    parser.add_argument("--stdin", action="store_true", help="check a PR body from stdin")
    args = parser.parse_args()

    bad = []
    if args.stdin:
        bad += [("PR body", line) for line in hits(sys.stdin.read())]
    else:
        base = args.rev_range.split("..")[0]
        rc, _ = git(args.root, "rev-parse", "--verify", "-q", base)
        if rc != 0:
            print("check-attribution: %s not found; no commit range to check." % base)
            return 0
        rc, out = git(args.root, "log", "--format=%H%x1f%B%x1e", args.rev_range)
        if rc != 0:
            print("check-attribution: git log %s failed" % args.rev_range, file=sys.stderr)
            return 1
        for entry in out.split("\x1e"):
            if "\x1f" not in entry:
                continue
            sha, body = entry.strip("\n").split("\x1f", 1)
            bad += [(sha.strip()[:10], line) for line in hits(body)]
    if bad:
        for where, line in bad:
            print("check-attribution: %s: %s" % (where, line), file=sys.stderr)
        print("check-attribution: remove the attribution (owner rule, AGENTS.md).", file=sys.stderr)
        return 1
    print("check-attribution: no AI attribution in %s." % ("the PR body" if args.stdin else args.rev_range))
    return 0


if __name__ == "__main__":
    sys.exit(main())
