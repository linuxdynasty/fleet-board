#!/usr/bin/env python3
"""Load the fleet contract from buf's compiled image.

One loader for every doc-drift script (plan row 1b): check-doc-names.py, and
later the tables generated from proto comments and the capability report. It
reads the JSON image that `buf build -o image.json` writes with the pinned buf
(BUF_VERSION in tool-versions.env), never the .proto text, so names, field
types and comments are exactly what buf compiled.

  load(root, image=None) -> Contract
      image: a prebuilt image JSON; else $FLEET_PROTO_IMAGE; else build one
      with the pinned buf in root (the tests point it at a fixture image).
  proto_image.py build --root DIR --out FILE     write the image
  proto_image.py summary [--root DIR] [--image FILE]

A Contract holds the elements of the fleet.* files only: messages (nested
too), enums and their values, services and their rpcs, oneofs and fields, each
with its leading and trailing comments; Contract.elements lists them all in
declaration order. Field types may point into imported
files (google.protobuf.Timestamp); those resolve but are not fleet names.

Fail closed: no `go`, a buf build error, an unreadable image, an image with no
fleet file, or a field type that is not in the image raises ProtoImageError.
"""
from __future__ import annotations

import argparse
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path

IMAGE_ENV = "FLEET_PROTO_IMAGE"
BUF_MODULE = "github.com/bufbuild/buf/cmd/buf"
FLEET_PREFIX = "fleet/"
# Field numbers in descriptor.proto, used by sourceCodeInfo paths.
FILE_MESSAGE, FILE_ENUM, FILE_SERVICE = 4, 5, 6
MSG_FIELD, MSG_NESTED, MSG_ENUM, MSG_ONEOF = 2, 3, 4, 8
ENUM_VALUE, SERVICE_METHOD = 2, 2
SEGMENT = re.compile(r"^([A-Za-z_][A-Za-z0-9_]*)(\[\])?$")


class ProtoImageError(Exception):
    """The contract could not be loaded; callers exit non-zero."""


class Element:
    def __init__(self, kind, name, full_name, file, loc):
        self.kind, self.name, self.full_name, self.file = kind, name, full_name, file
        self.comment = (loc or {}).get("leadingComments", "")
        self.trailing = (loc or {}).get("trailingComments", "")

    def __repr__(self):
        return "<%s %s>" % (self.kind, self.full_name)


class Message(Element):
    def __init__(self, *args):
        super().__init__("message", *args)
        self.fields, self.oneofs, self.nested = {}, {}, {}


class Field(Element):
    def __init__(self, raw, *args):
        super().__init__("field", *args)
        self.number = raw.get("number")
        self.repeated = raw.get("label") == "LABEL_REPEATED"
        self.type = raw.get("type", "").replace("TYPE_", "").lower()
        self.type_name = raw.get("typeName", "").lstrip(".") or None
        self.target = None  # Message or Enum, set once every file is indexed


class Oneof(Element):
    def __init__(self, *args):
        super().__init__("oneof", *args)
        self.fields = {}


class Enum(Element):
    def __init__(self, *args):
        super().__init__("enum", *args)
        self.values = {}
        self.prefix = upper_snake(self.name) + "_"

    def value(self, name):
        """The value named in full (DELIVERY_CLASS_NEVER_DROP) or by short form (NEVER_DROP)."""
        return self.values.get(name) or self.values.get(self.prefix + name)


class EnumValue(Element):
    def __init__(self, enum, raw, *args):
        super().__init__("enum value", *args)
        self.enum, self.number = enum, raw.get("number")
        self.short = self.name[len(enum.prefix):] if self.name.startswith(enum.prefix) else None


class Service(Element):
    def __init__(self, *args):
        super().__init__("service", *args)
        self.rpcs = {}


class Rpc(Element):
    def __init__(self, service, raw, *args):
        super().__init__("rpc", *args)
        self.service = service
        self.input = raw.get("inputType", "").lstrip(".")
        self.output = raw.get("outputType", "").lstrip(".")
        self.client_streaming = bool(raw.get("clientStreaming"))
        self.server_streaming = bool(raw.get("serverStreaming"))


class Scalar(Element):
    def __init__(self, field):
        super().__init__(field.type, field.name, field.full_name, field.file, None)


def upper_snake(name):
    return re.sub(r"(?<=[a-z0-9])(?=[A-Z])|(?<=[A-Z])(?=[A-Z][a-z])", "_", name).upper()


class Contract:
    def __init__(self, image):
        files = image.get("file") if isinstance(image, dict) else None
        if not isinstance(files, list):
            raise ProtoImageError("the image has no file list")
        self.types = {}  # every message and enum by full name, imported files included
        self.messages, self.enums, self.services = {}, {}, {}
        self.packages, self.by_name, self.values, self.short_values = set(), {}, {}, {}
        self.elements = []  # every fleet element in declaration order, fields and values too
        for f in files:
            self._index_file(f)
        if not self.packages:
            raise ProtoImageError("the image has no fleet.* file")
        for msg in self.types.values():
            for field in getattr(msg, "fields", {}).values():
                if field.type_name:
                    field.target = self.types.get(field.type_name)
                    if field.target is None:
                        raise ProtoImageError("%s refers to %s, which is not in the image" % (field.full_name, field.type_name))

    def _index_file(self, f):
        name, pkg = f.get("name", ""), f.get("package", "")
        fleet = name.startswith(FLEET_PREFIX)
        locs = {tuple(loc.get("path", ())): loc for loc in f.get("sourceCodeInfo", {}).get("location", [])}
        ctx = (name, pkg, fleet, locs)
        if fleet:
            self.packages.add(pkg)
        for i, raw in enumerate(f.get("messageType", [])):
            self._message(ctx, raw, pkg, (FILE_MESSAGE, i))
        for i, raw in enumerate(f.get("enumType", [])):
            self._enum(ctx, raw, pkg, (FILE_ENUM, i))
        for i, raw in enumerate(f.get("service", [])):
            self._service(ctx, raw, pkg, (FILE_SERVICE, i))

    def _add(self, ctx, element, table=None, named=True):
        if not ctx[2]:
            return
        element.order = len(self.elements)
        self.elements.append(element)
        if not named:
            return
        if table is not None:
            table[element.full_name] = element
        self.by_name.setdefault(element.name, []).append(element)

    def _message(self, ctx, raw, scope, path):
        name, _, _, locs = ctx
        msg = Message(raw["name"], scope + "." + raw["name"], name, locs.get(path))
        self.types[msg.full_name] = msg
        self._add(ctx, msg, self.messages)
        for i, o in enumerate(raw.get("oneofDecl", [])):
            oneof = Oneof(o["name"], msg.full_name + "." + o["name"], name, locs.get(path + (MSG_ONEOF, i)))
            msg.oneofs[oneof.name] = oneof
            self._add(ctx, oneof)
        oneof_list = list(msg.oneofs.values())
        for i, fr in enumerate(raw.get("field", [])):
            field = Field(fr, fr["name"], msg.full_name + "." + fr["name"], name, locs.get(path + (MSG_FIELD, i)))
            msg.fields[field.name] = field
            self._add(ctx, field, named=False)
            idx = fr.get("oneofIndex")
            if idx is not None and not fr.get("proto3Optional") and idx < len(oneof_list):
                oneof_list[idx].fields[field.name] = field
        for i, nr in enumerate(raw.get("nestedType", [])):
            msg.nested[nr["name"]] = self._message(ctx, nr, msg.full_name, path + (MSG_NESTED, i))
        for i, er in enumerate(raw.get("enumType", [])):
            msg.nested[er["name"]] = self._enum(ctx, er, msg.full_name, path + (MSG_ENUM, i))
        return msg

    def _enum(self, ctx, raw, scope, path):
        name, _, _, locs = ctx
        enum = Enum(raw["name"], scope + "." + raw["name"], name, locs.get(path))
        self.types[enum.full_name] = enum
        self._add(ctx, enum, self.enums)
        for i, vr in enumerate(raw.get("value", [])):
            value = EnumValue(enum, vr, vr["name"], scope + "." + vr["name"], name, locs.get(path + (ENUM_VALUE, i)))
            enum.values[value.name] = value
            self._add(ctx, value, named=False)
            if ctx[2]:
                self.values.setdefault(value.name, []).append(value)
                if value.short:
                    self.short_values.setdefault(value.short, []).append(value)
        return enum

    def _service(self, ctx, raw, scope, path):
        name, _, _, locs = ctx
        svc = Service(raw["name"], scope + "." + raw["name"], name, locs.get(path))
        self._add(ctx, svc, self.services)
        for i, mr in enumerate(raw.get("method", [])):
            rpc = Rpc(svc, mr, mr["name"], svc.full_name + "." + mr["name"], name, locs.get(path + (SERVICE_METHOD, i)))
            svc.rpcs[rpc.name] = rpc
            self._add(ctx, rpc)
        return svc

    def is_name(self, name):
        """A fleet message, enum, service, rpc or oneof has this name."""
        return name in self.by_name

    def is_enum_value(self, token):
        """A fleet enum value, in full or as its short form (the value minus <ENUM>_)."""
        return token in self.values or token in self.short_values

    def resolve(self, path):
        """Walk Type.field.sub (a trailing [] on a repeated field) hop by hop.

        Returns (element, None) or (None, reason). The first segment is a
        message, enum or service by short name, or a fleet.<pkg>. prefix.
        """
        segs = []
        for part in path.split("."):
            m = SEGMENT.match(part)
            if not m:
                return None, "not a name path"
            segs.append((m.group(1), bool(m.group(2))))
        starts, rest, first = None, segs[1:], segs[0][0]
        for pkg in sorted(self.packages, key=len, reverse=True):
            n = pkg.count(".") + 1
            if len(segs) > n and ".".join(s for s, _ in segs[:n]) == pkg:
                first = pkg + "." + segs[n][0]
                starts = [e for e in (self.types.get(first), self.services.get(first)) if e]
                rest = segs[n + 1:]
                break
        if starts is None:
            starts = [e for e in self.by_name.get(first, []) if e.kind in ("message", "enum", "service")]
        if not starts:
            return None, "no message, enum or service is named %s" % first
        reasons = []
        for start in starts:
            found, why = self._walk(start, rest)
            if found is not None:
                return found, None
            reasons.append(why)
        return None, reasons[0]

    def _walk(self, elem, rest):
        for name, brackets in rest:
            field = None
            if elem.kind == "message":
                field = elem.fields.get(name)
                if field is None:
                    nxt = elem.oneofs.get(name) or elem.nested.get(name)
                    if nxt is None or brackets:
                        return None, "%s has no field, oneof or nested type named %s" % (elem.name, name)
                    elem = nxt
                    continue
            elif elem.kind == "oneof":
                field = elem.fields.get(name)
                if field is None:
                    return None, "oneof %s has no field named %s" % (elem.full_name, name)
            elif elem.kind == "enum" and elem.value(name) and not brackets:
                elem = elem.value(name)
                continue
            elif elem.kind == "service" and name in elem.rpcs and not brackets:
                elem = elem.rpcs[name]
                continue
            else:
                return None, "%s is a %s and has no %s" % (elem.name, elem.kind, name)
            if brackets and not field.repeated:
                return None, "%s is not repeated, so [] does not apply" % field.full_name
            elem = field.target or Scalar(field)
        return elem, None


def buf_version(root):
    env_file = Path(root) / "tool-versions.env"
    try:
        for line in env_file.read_text().splitlines():
            if line.startswith("BUF_VERSION="):
                return line.split("=", 1)[1].strip()
    except OSError as err:
        raise ProtoImageError("cannot read %s: %s" % (env_file, err)) from err
    raise ProtoImageError("%s has no BUF_VERSION" % env_file)


def build(root, out):
    """Run the pinned buf in root and write its JSON image to out."""
    version = buf_version(root)
    go = shutil.which("go")
    if go is None:
        raise ProtoImageError("go is not on PATH, so the pinned buf cannot run")
    cmd = [go, "run", "%s@%s" % (BUF_MODULE, version), "build", "-o", str(out)]
    result = subprocess.run(cmd, cwd=str(root), capture_output=True, text=True)
    if result.returncode != 0:
        raise ProtoImageError("buf build failed (exit %d): %s" % (result.returncode, result.stderr.strip()))
    return out


def read_image(path):
    try:
        return json.loads(Path(path).read_text())
    except (OSError, ValueError) as err:
        raise ProtoImageError("cannot read the image %s: %s" % (path, err)) from err


def load(root, image=None):
    image = image or os.environ.get(IMAGE_ENV)
    if image:
        return Contract(read_image(image))
    with tempfile.TemporaryDirectory() as tmp:
        return Contract(read_image(build(root, Path(tmp) / "image.json")))


def main(argv):
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    sub = parser.add_subparsers(dest="cmd", required=True)
    b = sub.add_parser("build")
    b.add_argument("--root", default=".")
    b.add_argument("--out", required=True)
    s = sub.add_parser("summary")
    s.add_argument("--root", default=".")
    s.add_argument("--image")
    args = parser.parse_args(argv)
    try:
        if args.cmd == "build":
            build(Path(args.root).resolve(), Path(args.out).resolve())
            return 0
        c = load(Path(args.root).resolve(), args.image)
    except ProtoImageError as err:
        print("proto_image: " + str(err), file=sys.stderr)
        return 2
    rpcs = sum(len(s.rpcs) for s in c.services.values())
    print("packages %d, messages %d, enums %d, services %d, rpcs %d"
          % (len(c.packages), len(c.messages), len(c.enums), len(c.services), rpcs))
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
