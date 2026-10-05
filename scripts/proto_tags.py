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
capability table, or a tag key outside the block (trailing comments
included). ContractError means an anchor the rules need is missing; callers
exit 2 on it.
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
SPECIAL_SCOPES = ("self", "none", "per-topic")

SHAPED = re.compile(r"^\[([A-Z][A-Z_]*)(?=[ \]])")
TAG = re.compile(r"^\[(?P<key>[A-Z][A-Z_]*)(?: (?P<value>[^\]]+))?\](?: (?P<note>.*))?$")
IN_PROSE = re.compile(r"\[(%s)[ \]]" % "|".join(KEYS))
RELEASE_VALUE = re.compile(r"^(v1|v3) ([0-9]+[a-z]?|none)$")
CAP_ROW = re.compile(r"^\| ([A-Z][0-9]+) \|.*\| ([a-z0-9]+) \|\s*$")


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


def parse(comment, trailing=""):
    """(tags, problems) for one element's leading and trailing comments."""
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
    return tags, problems


def capability_ids(root):
    """[(id, phase)] in the order of the capability table in 01."""
    path = Path(root) / CAP_DOC
    try:
        rows = [CAP_ROW.match(line) for line in path.read_text().splitlines()]
    except OSError as err:
        raise ContractError("cannot read %s: %s" % (CAP_DOC, err)) from err
    caps = [(m.group(1), m.group(2)) for m in rows if m]
    if not caps:
        raise ContractError("%s has no capability table rows" % CAP_DOC)
    return caps


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
        tags, problems = parse(element.comment, element.trailing)
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

    def _check_scope(self, tag):
        parts = tag.value.split("|")
        if tag.value in SPECIAL_SCOPES:
            return []
        if any(p in SPECIAL_SCOPES for p in parts):
            return ["SCOPE %s: self, none and per-topic stand alone, never joined with |" % tag.value]
        return ["SCOPE %s: %s is not a Scope value" % (tag.value, p)
                for p in parts if p == "UNSPECIFIED" or self.scopes.value(p) is None]

    def _check_release(self, tag):
        if RELEASE_VALUE.match(tag.value):
            return []
        return ["RELEASE %s: RELEASE needs v1 or v3 and a plan row (12, 13a) or none" % tag.value]

    def _check_delivery(self, tag):
        cls, _, key = tag.value.partition(" ")
        if cls == "UNSPECIFIED" or self.classes.value(cls) is None:
            return ["DELIVERY %s: %s is not a DeliveryClass value" % (tag.value, cls)]
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
