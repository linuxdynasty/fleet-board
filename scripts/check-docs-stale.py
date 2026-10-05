#!/usr/bin/env python3
"""Report documentation that a change has left behind.

Three modes, one folder-to-spec map (.agents/doc-paths.json):

  check-docs-stale.py [--root DIR]
      Hook mode, for the PostToolUse hooks of Claude Code, Codex, Muse and
      Cursor and for a developer at any time. Read-only, prints one reminder
      per line or nothing, and never fails (exit 0), whatever goes wrong.
  check-docs-stale.py --ci --range A..B [--root DIR]
      Blocking mode (plan row 1b, make docs-stale-ci, CI). Exit 1 on a
      finding, 2 on an internal error: a git call that fails, a range that does
      not resolve, or a map that is unreadable or invalid.
  check-docs-stale.py --check-map [--root DIR]
      Validate the map; exit 1 on a problem (verify-agent-canon.sh runs it).

The map. Each rule names globs and specs; when a changed path matches a rule
(and none of its "exclude" globs), changing ANY ONE of its specs satisfies it.
Globs follow .agents/skill-paths.json (.claude/hooks/lib/skill_paths.py): a
glob with a "/" is anchored to the repo-relative path, "**/" is zero or more
directories, and a glob with no "/" matches any one path component. A rule may
name a folder that does not exist yet; every spec it names must exist.
"unmapped" lists folders no spec defines, with a reason; it is informational.

Blocking mode, over the files changed in A...B and the commits in A..B:
1. Mapped folders. A rule matched by a changed file needs one of its specs
   changed in the range, or a commit message line `No-Doc-Change: <reason>`
   (it satisfies every rule). Only the branch's own commits count (git log
   --first-parent A..B), never a commit merged in from main. An empty or
   placeholder reason (TODO, TBD, n/a, none, a dash, the word "reason", or the
   <reason> template itself) does not count. Only commit messages count, never
   a PR body.
2. Go package docs. A directory with a changed non-test .go file that is
   still a package at B carries doc.go, README.md and AGENTS.md (AGENTS.md,
   Documentation Discipline). The trailer does not excuse this.

Hook mode, from `git status` (so it needs no payload and works for any
harness, including Codex whose apply_patch payload names several files):
1. Go package docs: missing next to changed source, or older than it (the
   mtime comparison is advisory and hook-only).
2. Mapped folders: a working-tree change whose rule has no spec changed yet in
   the working tree or the branch.
Dirs under spikes/, vendor/ and testdata/ are skipped in both modes.
"""
from __future__ import annotations

import argparse
import json
import re
import subprocess
import sys
import time
from pathlib import Path

MAP_FILE = ".agents/doc-paths.json"
GO_DOCS = ("doc.go", "README.md", "AGENTS.md")
SKIP_PARTS = {"spikes", "vendor", "testdata"}
TRAILER = re.compile(r"^No-Doc-Change:(.*)$", re.M)
PLACEHOLDERS = {"", "todo", "tbd", "n/a", "na", "none", "-", "\u2014", "\u2013", "reason"}
sys.path.insert(0, str(Path(__file__).resolve().parents[1] / ".claude/hooks/lib"))


class GitError(Exception):
    pass


class MapError(Exception):
    pass


def git(root, *args, strict=False):
    result = subprocess.run(["git", "-C", str(root), *args], capture_output=True, text=True,
                            encoding="utf-8")
    if result.returncode != 0:
        if strict:
            raise GitError("git %s failed (exit %d): %s" % (" ".join(args), result.returncode, result.stderr.strip()))
        return ""
    return result.stdout


def glob_match(pattern, rel):
    from skill_paths import glob_match as match  # the one glob implementation, shared with skill-paths.json

    return match(pattern, rel)


def map_problems(root, check_specs=True):
    """Problems with the map, one string each; [] when it is valid."""
    try:
        data = json.loads((Path(root) / MAP_FILE).read_text(encoding="utf-8"))
    except (OSError, ValueError) as err:
        return [MAP_FILE + " is unreadable or not JSON: " + str(err)]
    if not isinstance(data, dict) or data.get("version") != 1:
        return [MAP_FILE + ": version must be 1"]
    rules = data.get("rules")
    if not isinstance(rules, list) or not rules:
        return [MAP_FILE + ": rules must be a non-empty list"]
    problems = []
    for n, rule in enumerate(rules, 1):
        where = "%s rule %d" % (MAP_FILE, n)
        if not isinstance(rule, dict):
            problems.append(where + ": must be an object")
            continue
        for key in ("globs", "specs"):
            value = rule.get(key)
            if not (isinstance(value, list) and value and all(isinstance(v, str) and v for v in value)):
                problems.append("%s: %s must be a non-empty list of strings" % (where, key))
        exclude = rule.get("exclude", [])
        if not (isinstance(exclude, list) and all(isinstance(v, str) and v for v in exclude)):
            problems.append(where + ": exclude must be a list of strings")
        for spec in rule.get("specs") or []:
            if check_specs and isinstance(spec, str) and spec and not (Path(root) / spec).is_file():
                problems.append("%s names %s, which does not exist" % (where, spec))
    unmapped = data.get("unmapped", {})
    if not (isinstance(unmapped, dict) and all(isinstance(v, str) and v.strip() for v in unmapped.values())):
        problems.append(MAP_FILE + ": unmapped must be an object of folder -> reason")
    return problems


def load_map(root, check_specs=True):
    problems = map_problems(root, check_specs)
    if problems:
        raise MapError("; ".join(problems))
    return json.loads((Path(root) / MAP_FILE).read_text(encoding="utf-8"))["rules"]


def matching_rules(rules, changed):
    """[(rule, [changed paths it matches])] for each rule some path matches."""
    hits = []
    for rule in rules:
        paths = [p for p in changed if any(glob_match(g, p) for g in rule["globs"])
                 and not any(glob_match(g, p) for g in rule.get("exclude", []))]
        if paths:
            hits.append((rule, paths))
    return hits


def short_list(paths, n=3):
    return ", ".join(paths[:n]) + (", ..." if len(paths) > n else "")


def go_source_dirs(changed):
    dirs = []
    for rel in changed:
        path = Path(rel)
        if path.suffix != ".go" or path.name == "doc.go" or path.name.endswith("_test.go"):
            continue
        if SKIP_PARTS & set(path.parts):
            continue
        if str(path.parent) not in dirs:
            dirs.append(str(path.parent))
    return sorted(dirs)


# ---- blocking mode -------------------------------------------------------------

def resolve_range(root, rng):
    parts = rng.split("...") if "..." in rng else rng.split("..")
    if len(parts) != 2 or not all(parts):
        raise GitError("cannot resolve the range %r: expected A..B" % rng)
    shas = []
    for ref in parts:
        sha = git(root, "rev-parse", "--verify", "--quiet", ref + "^{commit}").strip()
        if not sha:
            raise GitError("cannot resolve the range %r: %r is not a commit" % (rng, ref))
        shas.append(sha)
    return shas


def trailer_state(messages):
    """(valid reason or None, [placeholder lines seen])."""
    bad = []
    for message in messages:
        for m in TRAILER.finditer(message):
            reason = m.group(1).strip()
            norm = reason.rstrip(".").lower()
            if norm in PLACEHOLDERS or re.match(r"^(todo|tbd)\b", norm) or re.fullmatch(r"<[^>]*>", norm):
                bad.append(m.group(0).strip())
            else:
                return reason, bad
    return None, bad


def ci_findings(root, rng):
    git(root, "rev-parse", "--show-toplevel", strict=True)
    base, head = resolve_range(root, rng)
    rules = load_map(root)
    if git(root, "rev-list", "--count", base + ".." + head, strict=True).strip() == "0":
        return None  # an empty or reversed range has nothing to check
    changed = [p for p in git(root, "diff", "--name-only", "--no-renames", "-z", base + "..." + head,
                              strict=True).split("\0") if p]
    messages = [m for m in git(root, "log", "--first-parent", "--format=%B%x00", base + ".." + head,
                               strict=True).split("\0") if m.strip()]  # not commits merged in from main
    findings = []
    reason, placeholders = trailer_state(messages)
    if reason is None:
        done = set(changed)
        for rule, paths in matching_rules(rules, changed):
            if not done & set(rule["specs"]):
                findings.append("%s: changed in %s, but none of its specs did: %s. Update one, or add a line "
                                "`No-Doc-Change: <reason>` to a commit message"
                                % (short_list(paths), rng, ", ".join(rule["specs"])))
        if findings and placeholders:
            findings.append("`%s` does not count: an empty or placeholder reason (TODO, TBD, n/a, none, a dash, "
                            "reason, <reason>)"
                            % placeholders[0])
    for directory in go_source_dirs(changed):
        listed = git(root, "ls-tree", "-z", "--name-only", head, directory + "/", strict=True).split("\0")
        names = {Path(p).name for p in listed if p}
        if not any(n.endswith(".go") and not n.endswith("_test.go") for n in names):
            continue  # the package is gone at the end of the range
        for name in GO_DOCS:
            if name not in names:
                findings.append("%s: missing %s (every Go package carries doc.go, README.md and AGENTS.md)"
                                % (directory, name))
    return findings


def run_ci(root, rng):
    try:
        findings = ci_findings(root, rng)
    except (GitError, MapError) as err:
        print("check-docs-stale: " + str(err), file=sys.stderr)
        return 2
    except Exception as err:  # an internal error must fail the gate, never pass it
        print("check-docs-stale: internal error: %r" % err, file=sys.stderr)
        return 2
    if findings is None:
        print("check-docs-stale: no commits in %s; nothing to check" % rng)
        return 0
    for line in findings:
        print(line)
    if findings:
        print("check-docs-stale: %d finding(s) in %s (map: %s)" % (len(findings), rng, MAP_FILE), file=sys.stderr)
        return 1
    print("check-docs-stale: docs move with the code in %s" % rng)
    return 0


# ---- hook mode -----------------------------------------------------------------

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


def hook_go_findings(root, changed):
    findings = []
    for directory in go_source_dirs(changed):
        sources = [root / rel for rel in changed if go_source_dirs([rel]) == [directory]]
        newest = max((p.stat().st_mtime if p.exists() else time.time()) for p in sources)
        if not any(p.suffix == ".go" and not p.name.endswith("_test.go") for p in (root / directory).glob("*.go")):
            continue  # the package is gone
        for name in GO_DOCS:
            doc = root / directory / name
            if not doc.is_file():
                findings.append("%s: missing %s (every Go package carries doc.go, README.md and AGENTS.md)" % (directory, name))
            elif doc.stat().st_mtime < newest:
                findings.append("%s: %s is older than the Go source changed here; update it or confirm it is still true" % (directory, name))
    return findings


def hook_map_findings(root, changed, in_branch):
    try:
        rules = load_map(root, check_specs=False)  # advisory: a reminder needs no spec on disk
    except MapError:
        return []
    done = set(changed) | set(in_branch)
    return ["%s changed: update one of %s in the same PR, or add `No-Doc-Change: <reason>` to a commit (%s)"
            % (short_list(paths), ", ".join(rule["specs"]), MAP_FILE)
            for rule, paths in matching_rules(rules, changed) if not done & set(rule["specs"])]


def run_hook(root):
    changed = working_tree_changes(root)
    for line in hook_go_findings(root, changed) + hook_map_findings(root, changed, branch_changes(root)):
        print(line)
    return 0


def main():
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    parser.add_argument("--root", default=".")
    parser.add_argument("--ci", action="store_true", help="blocking mode; needs --range")
    parser.add_argument("--range", dest="rng", help="A..B: files changed in A...B, first-parent commit messages in A..B")
    parser.add_argument("--check-map", action="store_true", help="validate %s" % MAP_FILE)
    args = parser.parse_args()
    if args.ci or args.check_map:
        top = git(args.root, "rev-parse", "--show-toplevel").strip()
        root = Path(top or args.root).resolve()
        if args.check_map:
            problems = map_problems(root)
            for line in problems:
                print(line)
            return 1 if problems else 0
        if not args.rng:
            parser.error("--ci needs --range A..B")
        return run_ci(root, args.rng)
    try:
        root = Path(git(args.root, "rev-parse", "--show-toplevel").strip() or args.root).resolve()
        return run_hook(root)
    except Exception:  # a reminder tool must never break an edit
        return 0


if __name__ == "__main__":
    sys.exit(main())
