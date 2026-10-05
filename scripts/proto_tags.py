"""Read and check the comment tags in the fleet protos.

The grammar is in docs/specs/02e-contract-rules.md, "Comment tags"; this
module is its only reader. A comment opens with a tag block, one tag per line:

  [KEY value] note

The block ends at a blank `//` line or the first line that is not a tag; the
rest is prose. Keys: SCOPE and RELEASE on every FleetService rpc and every
WatchTopic value but the zero one, DELIVERY on every Event.body arm, CAP on any
message, field, enum value or rpc. Tags(contract, root) reads every fleet
element's comment and collects one problem per broken rule: an unknown key, a
malformed tag, a missing or misplaced tag, a value outside its enum or the 01
capability table, a [RELEASE] row that is not in the plan's PR sequence, or a
tag key outside the block (trailing comments and comments a blank line cuts
off included). ContractError means an anchor the rules need is missing, or
01's table or the plan cannot be read; callers exit 2 on it.
"""
from __future__ import annotations

import re
from pathlib import Path

KEYS = ("SCOPE", "RELEASE", "DELIVERY", "CAP")
FLEET_SERVICE = "fleet.hub.v1.FleetService"
WATCH_TOPIC = "fleet.hub.v1.WatchTopic"
SCOPE_ENUM = "fleet.hub.v1.Scope"
EVENT = "fleet.agent.v1.Event"
EVENT_ONEOF = "body"
DELIVERY_ENUM = "fleet.agent.v1.DeliveryClass"
CAP_DOC = "docs/specs/01-agent-capabilities.md"
PLAN_DOC = "docs/plan/v1-implementation-plan.md"
PLAN_SECTION = "## 5. The PR sequence"
SPECIAL_SCOPES = ("self", "none", "per-topic")

SHAPED = re.compile(r"^\[([A-Z][A-Z_]*)(?=[ \]])")
TAG = re.compile(r"^\[(?P<key>[A-Z][A-Z_]*)(?: (?P<value>[^\]]+))?\](?: (?P<note>.*))?$")
IN_PROSE = re.compile(r"\[(%s)[ \]]" % "|".join(KEYS))
RELEASE_VALUE = re.compile(r"^(v1|v3) ([0-9]+[a-z]?|none)$")
CAP_ROW = re.compile(r"^\| ([A-Z][0-9]+) \|.*\| ([a-z0-9]+) \|\s*$")
CAP_SHAPED = re.compile(r"^\| [A-Z][0-9]+ \|")
PLAN_ROW = re.compile(r"^\| ([0-9]+[a-z]?) \|")


class ContractError(Exception):
    """The contract or the capability table lacks something the rules need."""


class Tag:
    def __init__(self, key, value, note):
        self.key, self.value, self.note = key, value, note

    def __repr__(self):
        return "[%s %s] %s" % (self.key, self.value, self.note)


def comment_lines(text):
    lines = [line[1:] if line.startswith(" ") else line for line in (text or "").split("\n")]
    if lines and lines[-1] == "":
        lines.pop()
    return [line.rstrip() for line in lines]


def parse(comment, trailing="", detached=()):
    """(tags, problems) for one element's leading, trailing and detached comments."""
    tags, problems, lines = [], [], comment_lines(comment)
    i = 0
    while i < len(lines) and SHAPED.match(lines[i]):
        key = SHAPED.match(lines[i]).group(1)
        m = TAG.match(lines[i])
        if key not in KEYS:
            problems.append("unknown tag key %s (02e, Comment tags)" % key)
        elif m is None or not m.group("value"):
            problems.append("malformed tag %r: write [%s value] note" % (lines[i], key))
        else:
            tags.append(Tag(key, m.group("value"), (m.group("note") or "").strip()))
        i += 1
    for line in lines[i:] + comment_lines(trailing):
        for m in IN_PROSE.finditer(line):
            problems.append("tag key [%s] outside the tag block" % m.group(1))
    for text in detached:
        for line in comment_lines(text):
            for m in IN_PROSE.finditer(line):
                problems.append("tag key [%s] in a comment a blank line cuts off; join it to the element's comment"
                                % m.group(1))
    return tags, problems


def capability_ids(root):
    """[(id, phase)] in the order of the capability table in 01."""
    try:
        lines = (Path(root) / CAP_DOC).read_text(encoding="utf-8").splitlines()
    except OSError as err:
        raise ContractError("cannot read %s: %s" % (CAP_DOC, err)) from err
    caps = []
    for n, line in enumerate(lines, 1):
        if not CAP_SHAPED.match(line):
            continue
        m = CAP_ROW.match(line)
        if m is None:
            raise ContractError("%s line %d: a capability row must read | ID | Capability | Kind | Phase |" % (CAP_DOC, n))
        if m.group(1) in {cid for cid, _ in caps}:
            raise ContractError("%s line %d: %s is listed twice" % (CAP_DOC, n, m.group(1)))
        caps.append((m.group(1), m.group(2)))
    if not caps:
        raise ContractError("%s has no capability table rows" % CAP_DOC)
    return caps


def plan_rows(root):
    """The row ids (12, 13a) of the PR sequence: the first table under section 5."""
    try:
        text = (Path(root) / PLAN_DOC).read_text(encoding="utf-8")
    except OSError as err:
        raise ContractError("cannot read %s: %s" % (PLAN_DOC, err)) from err
    if PLAN_SECTION not in text:
        raise ContractError("%s has no section %r" % (PLAN_DOC, PLAN_SECTION))
    section = text.split(PLAN_SECTION, 1)[1].split("\n## ", 1)[0].splitlines()
    first = next((n for n, line in enumerate(section) if line.startswith("|")), len(section))
    table = []
    for line in section[first:]:
        if not line.startswith("|"):
            break  # only the first table is the PR sequence
        table.append(line)
    rows = {m.group(1) for m in map(PLAN_ROW.match, table) if m}
    if not rows:
        raise ContractError("%s section %r has no PR rows" % (PLAN_DOC, PLAN_SECTION))
    return rows


def local_name(contract, full):
    for pkg in sorted(contract.packages, key=len, reverse=True):
        if full.startswith(pkg + "."):
            return full[len(pkg) + 1:]
    return full


def article(word):
    return "an" if word[0] in "aeiou" else "a"


class Tags:
    def __init__(self, contract, root):
        self.contract = contract
        self.capabilities = capability_ids(root)
        self.cap_ids = {cid for cid, _ in self.capabilities}
        self.plan_rows = plan_rows(root)
        self.service = self._anchor(contract.services, FLEET_SERVICE)
        self.topics = self._anchor(contract.enums, WATCH_TOPIC)
        self.scopes = self._anchor(contract.enums, SCOPE_ENUM)
        self.classes = self._anchor(contract.enums, DELIVERY_ENUM)
        event = self._anchor(contract.messages, EVENT)
        if EVENT_ONEOF not in event.oneofs:
            raise ContractError("the contract has no %s.%s oneof" % (EVENT, EVENT_ONEOF))
        self.event = event
        self.rpcs = list(self.service.rpcs.values())
        self.topic_values = [v for v in self.topics.values.values() if v.number != 0]
        self.arms = list(event.oneofs[EVENT_ONEOF].fields.values())
        scoped = {id(e) for e in self.rpcs + self.topic_values}
        arms = {id(e) for e in self.arms}
        self.by_element, self.problems = {}, []
        for element in contract.elements:
            self._read(element, id(element) in scoped, id(element) in arms)

    def _anchor(self, table, full):
        if full not in table:
            raise ContractError("the contract has no %s, which the comment tags need" % full)
        return table[full]

    def label(self, element):
        name = local_name(self.contract, element.full_name)
        if element.kind == "enum value":
            name = local_name(self.contract, element.enum.full_name) + "." + element.name
        return "api/%s: %s %s" % (element.file, element.kind, name)

    def _read(self, element, scoped, arm):
        tags, problems = parse(element.comment, element.trailing, element.detached)
        keys = [t.key for t in tags]
        allowed = {"CAP"} if element.kind in ("message", "field", "enum value", "rpc") else set()
        if scoped:
            allowed |= {"SCOPE", "RELEASE"}
        if arm:
            allowed.add("DELIVERY")
        for key in sorted(set(keys) - allowed):
            problems.append("[%s] is not allowed on %s %s" % (key, article(element.kind), element.kind))
        required = (["SCOPE", "RELEASE"] if scoped else []) + (["DELIVERY"] if arm else [])
        for key in required:
            if key not in keys:
                problems.append("needs a [%s] tag" % key)
        for key in ("SCOPE", "DELIVERY", "CAP"):
            if keys.count(key) > 1:
                problems.append("has more than one [%s] tag" % key)
        for tag in tags:
            if tag.key in allowed:
                problems.extend(getattr(self, "_check_" + tag.key.lower())(tag))
        releases = {t.value.split()[0] for t in tags if t.key == "RELEASE" and RELEASE_VALUE.match(t.value)}
        if len(releases) > 1:
            problems.append("all [RELEASE] lines of one element name one release")
        rows = [t.value for t in tags if t.key == "RELEASE"]
        if len(rows) != len(set(rows)):
            problems.append("names one [RELEASE] plan row twice")
        self.problems.extend("%s: %s" % (self.label(element), p) for p in problems)
        if tags:
            self.by_element[id(element)] = tags

    def _enum_problem(self, enum, value):
        """Why value is not a short form of one of enum's values, or None."""
        if value.startswith(enum.prefix):
            return "write %s without %s" % (value, enum.prefix)
        if value == "UNSPECIFIED":
            return "UNSPECIFIED is never a tag value"
        if enum.prefix + value not in enum.values:
            return "%s is not %s %s value" % (value, article(enum.name.lower()), enum.name)
        return None

    def _check_scope(self, tag):
        parts = tag.value.split("|")
        if tag.value in SPECIAL_SCOPES:
            return []
        if any(p in SPECIAL_SCOPES for p in parts):
            return ["SCOPE %s: self, none and per-topic stand alone, never joined with |" % tag.value]
        found = (self._enum_problem(self.scopes, p) for p in parts)
        return ["SCOPE %s: %s" % (tag.value, why) for why in found if why]

    def _check_release(self, tag):
        m = RELEASE_VALUE.match(tag.value)
        if not m:
            return ["RELEASE %s: RELEASE needs v1 or v3 and a plan row (12, 13a) or none" % tag.value]
        release, row = tag.value.split()
        if release == "v3" and row != "none":
            return ["RELEASE %s: v3 has no plan row yet; write [RELEASE v3 none]" % tag.value]
        if release == "v1" and row == "none":
            return ["RELEASE %s: v1 names a plan row (12, 13a)" % tag.value]
        if release == "v1" and row not in self.plan_rows:
            return ["RELEASE %s: %s is not a row of the PR sequence in %s" % (tag.value, row, PLAN_DOC)]
        return []

    def _check_delivery(self, tag):
        cls, _, key = tag.value.partition(" ")
        why = self._enum_problem(self.classes, cls)
        if why:
            return ["DELIVERY %s: %s" % (tag.value, why)]
        if cls == "COALESCE" and not key.strip():
            return ["DELIVERY %s: COALESCE needs a key" % tag.value]
        if cls != "COALESCE" and key:
            return ["DELIVERY %s: %s takes no key" % (tag.value, cls)]
        return []

    def _check_cap(self, tag):
        problems = ["CAP %s: %s is not a capability id in %s" % (tag.value, cid, CAP_DOC)
                    for cid in tag.value.split() if cid not in self.cap_ids]
        if tag.note:
            problems.append("CAP %s: a [CAP] tag has no note" % tag.value)
        return problems

    def tags(self, element, key):
        return [t for t in self.by_element.get(id(element), []) if t.key == key]

    def one(self, element, key):
        found = self.tags(element, key)
        return found[0] if found else None

    def caps(self, element):
        return [cid for t in self.tags(element, "CAP") for cid in t.value.split()]
