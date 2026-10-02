#!/usr/bin/env python3
"""Map repository paths to the project skills that govern them.

The map is data, in .agents/skill-paths.json, so a path or skill change is one
JSON edit. skill-nudge.sh calls `match`; scripts/verify-agent-canon.sh calls
`check`.

  skill_paths.py match ROOT RELPATH   line 1: space-separated skill ids
                                      line 2: notes joined with " ; "
  skill_paths.py check ROOT           prints one problem per line; exit 1 if any

Rules are a union: every rule whose globs match contributes its skills, so
order does not matter (eshu's first-match arms made order load-bearing; this
avoids that). Glob syntax, matched against the path relative to the repo root:

  *   any characters except /          **/  zero or more directories
  **  anything, including /            ?    one character except /

A glob with no "/" matches any single path component (like .gitignore), so
"*sqlite*" matches spikes/sqlite/main.go and "go.mod" matches sub/go.mod.
A glob with a "/" is anchored to the whole relative path. A rule's optional
"exclude" globs remove a path from that rule only.
"""
from __future__ import annotations

import json
import re
import sys
from pathlib import Path

MAP_FILE = ".agents/skill-paths.json"


def glob_regex(pattern):
    out, i = [], 0
    while i < len(pattern):
        if pattern.startswith("**/", i):
            out.append("(?:.*/)?")
            i += 3
        elif pattern.startswith("**", i):
            out.append(".*")
            i += 2
        elif pattern[i] == "*":
            out.append("[^/]*")
            i += 1
        elif pattern[i] == "?":
            out.append("[^/]")
            i += 1
        else:
            out.append(re.escape(pattern[i]))
            i += 1
    return re.compile("".join(out) + r"\Z")


def glob_match(pattern, rel):
    regex = glob_regex(pattern)
    if "/" in pattern:
        return regex.match(rel) is not None
    return any(regex.match(part) for part in rel.split("/"))


def load(root):
    return json.loads((Path(root) / MAP_FILE).read_text())


def match(root, rel):
    skills, notes = [], []
    for rule in load(root)["rules"]:
        if not any(glob_match(g, rel) for g in rule["globs"]):
            continue
        if any(glob_match(g, rel) for g in rule.get("exclude", [])):
            continue
        for skill in rule["skills"]:
            if skill not in skills:
                skills.append(skill)
        if rule.get("note") and rule["note"] not in notes:
            notes.append(rule["note"])
    return skills, notes


def check(root):
    problems = []
    try:
        data = load(root)
    except (OSError, ValueError) as err:
        return [MAP_FILE + " is unreadable or not JSON: " + str(err)]
    if data.get("version") != 1:
        problems.append(MAP_FILE + ": version must be 1")
    skills_dir = Path(root) / ".agents" / "skills"
    known = {p.parent.name for p in skills_dir.glob("*/SKILL.md")}
    mapped = set()
    rules = data.get("rules")
    if not isinstance(rules, list) or not rules:
        problems.append(MAP_FILE + ": rules must be a non-empty list")
        rules = []
    for n, rule in enumerate(rules, 1):
        where = MAP_FILE + " rule " + str(n)
        names, globs = rule.get("skills"), rule.get("globs")
        if not (isinstance(names, list) and names and all(isinstance(s, str) for s in names)):
            problems.append(where + ": skills must be a non-empty list of strings")
            continue
        if not (isinstance(globs, list) and globs and all(isinstance(g, str) for g in globs)):
            problems.append(where + ": globs must be a non-empty list of strings")
        for skill in names:
            mapped.add(skill)
            if skill not in known:
                problems.append(where + " names skill " + skill + ", which does not exist in .agents/skills")
    exempt = data.get("exempt", {})
    if not isinstance(exempt, dict):
        problems.append(MAP_FILE + ": exempt must be an object of skill -> reason")
        exempt = {}
    for skill, reason in exempt.items():
        if skill not in known:
            problems.append(MAP_FILE + " exempts " + skill + ", which does not exist in .agents/skills")
        if not (isinstance(reason, str) and reason.strip()):
            problems.append(MAP_FILE + ": exempt " + skill + " needs a reason")
        if skill in mapped:
            problems.append(MAP_FILE + ": " + skill + " is both mapped and exempt")
    for skill in sorted(known - mapped - set(exempt)):
        problems.append(MAP_FILE + ": skill " + skill + " is neither mapped nor exempt, so edits can never nudge it")
    return problems


def main(argv):
    if len(argv) == 4 and argv[1] == "match":
        try:
            skills, notes = match(argv[2], argv[3])
        except (OSError, ValueError, KeyError, TypeError):
            return 0
        print(" ".join(skills))
        print(" ; ".join(notes))
        return 0
    if len(argv) == 3 and argv[1] == "check":
        problems = check(argv[2])
        for problem in problems:
            print(problem)
        return 1 if problems else 0
    print(__doc__.split("\n\n")[0], file=sys.stderr)
    return 2


if __name__ == "__main__":
    sys.exit(main(sys.argv))
