#!/usr/bin/env python3
"""Fail when a backticked proto name in the docs is not in api/fleet.

Plan row 1b (ADR 0025 decision 7). The contract comes from buf's compiled
image through scripts/proto_image.py (the pinned buf; $FLEET_PROTO_IMAGE or
--image points at a prebuilt image instead).

Usage: scripts/check-doc-names.py [--root DIR] [--image FILE]
Exit 0 clean; 1 an unknown name or a bad allowlist entry (one line each,
file:line: `token` (why)); 2 the contract or the allowlist could not be read.

Checked: docs/specs/**, docs/plan/**, docs/development/**, docs/design.md,
docs/features.md, docs/adr/README.md, docs/flows/README.md and
docs/design-deployment-and-milestones.md. Not checked: the accepted ADRs
(docs/adr/NNNN-*.md, never edited), docs/reviews/** and docs/research/**.

Only inline code spans count; fenced code blocks are skipped. A span is
skipped whole when it holds a `/` (paths, URLs) or ends in a file extension
(README.md, events.proto). Inside a span, a token joined to `-`, `<` or `>`
(X-Forwarded-For, tmux -L, <NAME>, TRIAGE_KIND_<NAME>) is not an identifier
and is skipped. Every other token is checked by its shape:

  UpperCamel             a message, enum, service, rpc or oneof name
  SCREAMING_SNAKE        an enum value, in full or as its short form (the value
                         minus <ENUM>_: NEVER_DROP, READ); one letter is not a token
  Type.field.sub[]       resolves hop by hop through field types ([] only on a
                         repeated field); Enum.VALUE, Service.Rpc, Message.Nested
  fleet.pkg.v1[.Type...] a fleet package, or a path that resolves under it

lower_snake names and other lower-case-led tokens (os.Getenv) are not checked.
A token that is not a proto name on purpose (a hook event, a Go type, an env
var, a gRPC code) needs an entry in .agents/doc-names-allow.json: the exact
token, a kind and a reason. An entry no checked doc uses fails, and so does an
entry that is a real proto name.
"""
from __future__ import annotations

import argparse
import json
import re
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import proto_image  # noqa: E402

ALLOW_FILE = ".agents/doc-names-allow.json"
CHECKED_DIRS = ("docs/specs", "docs/plan", "docs/development")
CHECKED_FILES = ("docs/design.md", "docs/features.md", "docs/adr/README.md", "docs/flows/README.md",
                 "docs/design-deployment-and-milestones.md")
# A span ending in one of these is a file name. None of them is a fleet field name.
EXTENSIONS = ("md", "proto", "go", "json", "jsonl", "yaml", "yml", "sh", "py", "ts", "tsx", "js", "mjs",
              "db", "toml", "txt", "html", "css", "sql", "env", "mod", "sum", "lock", "pem", "plist",
              "zst", "gz", "tar", "csv", "svg", "png", "wasm", "conf", "socket", "timer")
FILE_NAME = re.compile(r"\.(?:%s)$" % "|".join(EXTENSIONS))
FENCE = re.compile(r"^\s*(`{3,}|~{3,})")
SPAN = re.compile(r"(?<!`)(`+)(?!`)(.+?)(?<!`)\1(?!`)", re.S)
TOKEN = re.compile(r"(?<!\w)[A-Za-z_]\w*(?:\[\])?(?:\.[A-Za-z_]\w*(?:\[\])?)*")
UPPER_CAMEL = re.compile(r"^[A-Z][A-Za-z0-9]*[a-z][A-Za-z0-9]*$")
SCREAMING = re.compile(r"^[A-Z][A-Z0-9]*(?:_[A-Z0-9]+)*$")
JOINED = "-<>"


class AllowlistError(Exception):
    pass


def checked_docs(root):
    docs = []
    for d in CHECKED_DIRS:
        docs += sorted(p for p in (root / d).rglob("*.md") if p.is_file())
    docs += [root / f for f in CHECKED_FILES if (root / f).is_file()]
    return docs


def blocks(text):
    """Yield (first line number, text) for each run of prose lines outside fences.

    A blank line ends a block and so does each table row, so a stray backtick
    cannot pair with one on a later row.
    """
    fence, start, buf = None, 0, []
    for n, line in enumerate(text.split("\n"), 1):
        m = FENCE.match(line)
        if fence:
            if m and m.group(1)[0] == fence[0] and len(m.group(1)) >= len(fence):
                fence = None
            continue
        if m:
            if buf:
                yield start, "\n".join(buf)
            fence, buf = m.group(1), []
            continue
        if not line.strip() or line.lstrip().startswith("|"):
            if buf:
                yield start, "\n".join(buf)
            buf = []
            if line.strip():
                yield n, line
            continue
        if not buf:
            start = n
        buf.append(line)
    if buf and not fence:
        yield start, "\n".join(buf)


def span_tokens(text):
    """Yield (offset, token) for each token this check looks at."""
    for m in SPAN.finditer(text):
        span = m.group(2)
        stripped = span.strip()
        if "/" in span or FILE_NAME.search(stripped) or stripped.startswith("www."):
            continue
        for t in TOKEN.finditer(span):
            before = span[t.start() - 1] if t.start() else ""
            after = span[t.end()] if t.end() < len(span) else ""
            if (before and before in JOINED) or (after and after in JOINED):
                continue
            yield m.start(2) + t.start(), t.group(0)


def judge(contract, token):
    """(checked, why): why is None when the token is a real proto name."""
    segs = token.split(".")
    if len(segs) == 1 and not token.endswith("[]"):
        if UPPER_CAMEL.match(token):
            return True, None if contract.is_name(token) else "no message, enum, service, rpc or oneof has this name"
        if SCREAMING.match(token) and len(token) > 1:
            return True, None if contract.is_enum_value(token) else "no enum value has this name or short form"
        return False, None
    head = segs[0].rstrip("[]")
    if token in contract.packages:
        return True, None
    if UPPER_CAMEL.match(head) or any(token.startswith(p + ".") for p in contract.packages):
        _, why = contract.resolve(token)
        return True, why
    if SCREAMING.match(head):
        bad = [s for s in segs if SCREAMING.match(s) and len(s) > 1 and not contract.is_enum_value(s)]
        return True, ("no enum value named %s" % ", ".join(bad)) if bad else None
    return False, None


def load_allowlist(root):
    path = root / ALLOW_FILE
    try:
        data = json.loads(path.read_text())
    except (OSError, ValueError) as err:
        raise AllowlistError("%s is unreadable or not JSON: %s" % (ALLOW_FILE, err)) from err
    if not isinstance(data, dict) or data.get("version") != 1 or not isinstance(data.get("entries"), list):
        raise AllowlistError("%s needs version 1 and an entries list" % ALLOW_FILE)
    allow = {}
    for n, entry in enumerate(data["entries"], 1):
        fields = [entry.get(k) if isinstance(entry, dict) else None for k in ("token", "kind", "reason")]
        if not all(isinstance(v, str) and v.strip() for v in fields):
            raise AllowlistError("%s entry %d needs a token, a kind and a reason" % (ALLOW_FILE, n))
        if fields[0] in allow:
            raise AllowlistError("%s lists %s twice" % (ALLOW_FILE, fields[0]))
        allow[fields[0]] = entry
    return allow


def check(root, contract, allow):
    findings, used, checked = [], set(), 0
    docs = checked_docs(root)
    for doc in docs:
        rel = doc.relative_to(root).as_posix()
        for start, text in blocks(doc.read_text()):
            for offset, token in span_tokens(text):
                is_checked, why = judge(contract, token)
                if not is_checked:
                    continue
                checked += 1
                if why is None:
                    continue
                if token in allow:
                    used.add(token)
                    continue
                findings.append("%s:%d: `%s` (%s)" % (rel, start + text.count("\n", 0, offset), token, why))
    for token in allow:
        is_checked, why = judge(contract, token)
        if is_checked and why is None:
            findings.append("%s: `%s` is a real proto name; remove the entry" % (ALLOW_FILE, token))
        elif token not in used:
            findings.append("%s: `%s`: no checked doc uses it; remove the entry" % (ALLOW_FILE, token))
    return findings, len(docs), checked


def main(argv):
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    parser.add_argument("--root", default=str(Path(__file__).resolve().parents[1]))
    parser.add_argument("--image", help="a prebuilt buf image (JSON); default $%s, else build" % proto_image.IMAGE_ENV)
    args = parser.parse_args(argv)
    root = Path(args.root).resolve()
    try:
        allow = load_allowlist(root)
        contract = proto_image.load(root, args.image)
    except (AllowlistError, proto_image.ProtoImageError) as err:
        print("check-doc-names: " + str(err), file=sys.stderr)
        return 2
    findings, ndocs, nchecked = check(root, contract, allow)
    for line in findings:
        print(line)
    if findings:
        print("check-doc-names: %d problem(s); fix the name, or allowlist a deliberate non-proto token in %s"
              % (len(findings), ALLOW_FILE), file=sys.stderr)
        return 1
    print("check-doc-names: %d docs, %d names checked, all in api/fleet or allowlisted" % (ndocs, nchecked))
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
