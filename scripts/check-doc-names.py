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

Only inline code spans count; fenced code blocks are skipped, and a fence
that never closes is a finding (the rest of that file is not checked). Inside a
span every token is checked except the ones that are not names: a URL
(https://..., www....), a file name (README.md, events.proto), a word joined to
`-` (X-Forwarded-For, SHA-256, tmux -L), a placeholder in <...> that is
SCREAMING or lower-case (<NAME>, <reason>), and the pieces of a template glued
to one (TRIAGE_KIND_<NAME>, <Rpc>Request). `/` and `->` only separate names, so
ListSessions/Foo, A->B, GET /fleet.hub.v1.FleetService/AttachTerminal,
<GetFleetSummary> and map<string, Duration> are all checked. Each token is
checked by its shape:

  UpperCamel             a message, enum, service, rpc or oneof name
  SCREAMING_SNAKE        an enum value, in full or as its short form (the value
                         minus <ENUM>_: NEVER_DROP, READ); one letter is not a token
  Type.field.sub[]       resolves hop by hop through field types ([] only on a
                         repeated field); Enum.VALUE, Service.Rpc, Message.Nested
  fleet.pkg.v1[.Type...] a fleet package, or a path that resolves under it; any
                         other fleet.* token is a finding

  Upper_With_Under       starts upper-case, has a lower-case letter and a _: no
                         proto name has that shape, so it is a finding

lower_snake names and other lower-case-led tokens (os.Getenv) are not checked.
A token that is not a proto name on purpose (a hook event, a Go type, an env
var, a gRPC code) needs an entry in .agents/doc-names-allow.json: the exact
token, a kind and a reason, and optionally a file that limits it to one doc
(the token elsewhere is then a finding). An entry no checked doc uses fails
(a file-limited one fails when its file does not use it), and so does an entry
that is a real proto name.
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
MIXED = re.compile(r"^[A-Z]\w*$")  # starts upper-case, not SCREAMING: Foo_Bar is no proto name shape
ARROW = re.compile(r"->|=>")
URL = re.compile(r"\b[A-Za-z][A-Za-z0-9+.-]*://\S*|\bwww\.\S*")


class AllowlistError(Exception):
    pass


def checked_docs(root):
    docs = []
    for d in CHECKED_DIRS:
        docs += sorted(p for p in (root / d).rglob("*.md") if p.is_file())
    docs += [root / f for f in CHECKED_FILES if (root / f).is_file()]
    return docs


def blocks(text, unclosed=None):
    """Yield (first line number, text) for each run of prose lines outside fences.

    A blank line ends a block and so does each table row, so a stray backtick
    cannot pair with one on a later row. The line of a fence that never closes
    is appended to unclosed.
    """
    fence, fence_line, start, buf = None, 0, 0, []
    for n, line in enumerate(text.split("\n"), 1):
        m = FENCE.match(line)
        if fence:
            if m and m.group(1)[0] == fence[0] and len(m.group(1)) >= len(fence):
                fence = None
            continue
        if m:
            if buf:
                yield start, "\n".join(buf)
            fence, fence_line, buf = m.group(1), n, []
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
    if fence and unclosed is not None:
        unclosed.append(fence_line)


def angle_group(span, start, end):
    """(index of <, index of >) when the token at start:end sits inside <...>, else None."""
    opened = max(span.rfind("<", 0, start), span.rfind(">", 0, start))
    if opened < 0 or span[opened] != "<":
        return None
    closes = [i for i in (span.find("<", end), span.find(">", end)) if i >= 0]
    if not closes or span[min(closes)] != ">":
        return None
    return opened, min(closes)


def is_name_part(span, start, end, token):
    """False for a token that is not a name: a URL, a file name, a word joined to -, a placeholder."""
    if FILE_NAME.search(token):
        return False  # README.md, fleet.yaml
    before = span[start - 1] if start else ""
    after = span[end] if end < len(span) else ""
    if "-" in (before, after):
        return False  # X-Forwarded-For, SHA-256, tmux -L
    if before == ">" or after == "<":
        return False  # outside a placeholder: TRIAGE_KIND_<NAME>, <Rpc>Request
    group = angle_group(span, start, end)
    if group is None:
        return True
    close = group[1]
    if close + 1 < len(span) and (span[close + 1].isalnum() or span[close + 1] == "_"):
        return False  # <Rpc>Request: a template, not a name
    return token[0].isupper() and not SCREAMING.match(token)  # <NAME>, <reason> are placeholders


def span_tokens(text):
    """Yield (offset, token) for each token this check looks at."""
    for m in SPAN.finditer(text):
        span = ARROW.sub(lambda a: " " * len(a.group(0)), m.group(2))
        urls = [u.span() for u in URL.finditer(span)]
        for t in TOKEN.finditer(span):
            if any(a <= t.start() < b for a, b in urls):
                continue
            if is_name_part(span, t.start(), t.end(), t.group(0)):
                yield m.start(2) + t.start(), t.group(0)


def mixed(token):
    return bool(MIXED.match(token)) and "_" in token and any(c.islower() for c in token)


def judge(contract, token):
    """(checked, why): why is None when the token is a real proto name."""
    segs = token.split(".")
    if len(segs) == 1 and not token.endswith("[]"):
        if UPPER_CAMEL.match(token):
            return True, None if contract.is_name(token) else "no message, enum, service, rpc or oneof has this name"
        if SCREAMING.match(token) and len(token) > 1:
            return True, None if contract.is_enum_value(token) else "no enum value has this name or short form"
        if mixed(token):
            return True, "not a proto name shape (UpperCamel names have no _)"
        return False, None
    head = segs[0].rstrip("[]")
    if token in contract.packages:
        return True, None
    if UPPER_CAMEL.match(head) or mixed(head) or any(token.startswith(p + ".") for p in contract.packages):
        _, why = contract.resolve(token)
        return True, why
    if head == "fleet":
        return True, "no fleet package matches"
    if SCREAMING.match(head):
        bad = [s for s in segs if SCREAMING.match(s) and len(s) > 1 and not contract.is_enum_value(s)]
        return True, ("no enum value named %s" % ", ".join(bad)) if bad else None
    return False, None


def load_allowlist(root):
    path = root / ALLOW_FILE
    try:
        data = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, ValueError) as err:
        raise AllowlistError("%s is unreadable or not JSON: %s" % (ALLOW_FILE, err)) from err
    if not isinstance(data, dict) or data.get("version") != 1 or not isinstance(data.get("entries"), list):
        raise AllowlistError("%s needs version 1 and an entries list" % ALLOW_FILE)
    allow = {}
    for n, entry in enumerate(data["entries"], 1):
        fields = [entry.get(k) if isinstance(entry, dict) else None for k in ("token", "kind", "reason")]
        if not all(isinstance(v, str) and v.strip() for v in fields):
            raise AllowlistError("%s entry %d needs a token, a kind and a reason" % (ALLOW_FILE, n))
        if "file" in entry and not (isinstance(entry["file"], str) and entry["file"].strip()):
            raise AllowlistError("%s entry %d: file must be a doc path" % (ALLOW_FILE, n))
        if fields[0] in allow:
            raise AllowlistError("%s lists %s twice" % (ALLOW_FILE, fields[0]))
        allow[fields[0]] = entry
    return allow


def allowed(allow, token, rel):
    entry = allow.get(token)
    return entry is not None and entry.get("file", rel) == rel


def check(root, contract, allow):
    findings, used, checked = [], set(), 0
    docs = checked_docs(root)
    for doc in docs:
        rel = doc.relative_to(root).as_posix()
        unclosed = []
        for start, text in blocks(doc.read_text(encoding="utf-8"), unclosed):
            for offset, token in span_tokens(text):
                is_checked, why = judge(contract, token)
                if not is_checked:
                    continue
                checked += 1
                if why is None:
                    continue
                if allowed(allow, token, rel):
                    used.add(token)
                    continue
                findings.append("%s:%d: `%s` (%s)" % (rel, start + text.count("\n", 0, offset), token, why))
        findings += ["%s:%d: a code fence that never closes; the rest of the file is not checked" % (rel, n)
                     for n in unclosed]
    for token, entry in allow.items():
        is_checked, why = judge(contract, token)
        if is_checked and why is None:
            findings.append("%s: `%s` is a real proto name; remove the entry" % (ALLOW_FILE, token))
        elif token not in used and "file" in entry:
            findings.append("%s: `%s`: %s does not use it; remove the entry or fix its file"
                            % (ALLOW_FILE, token, entry["file"]))
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
