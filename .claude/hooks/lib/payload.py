#!/usr/bin/env python3
"""Print fields of a hook payload (JSON on stdin), one value per line.

Usage: payload.py FIELD...

A FIELD is a dotted path (tool_input.file_path); "a|b" tries alternatives in
order, which is how one script reads both Claude's tool_input.file_path and
Muse's tool_input.path. A missing field, or input that is not a JSON object,
prints an empty line so callers can read a fixed number of lines. Newlines in
values become spaces; booleans print as true or false.
"""
from __future__ import annotations

import json
import sys


def dig(data, dotted):
    for key in dotted.split("."):
        if not isinstance(data, dict) or key not in data:
            return None
        data = data[key]
    return data


def render(value):
    if value is None:
        return ""
    if isinstance(value, bool):
        return "true" if value else "false"
    if isinstance(value, (dict, list)):
        return ""
    return str(value).replace("\r", " ").replace("\n", " ")


def main(fields):
    try:
        data = json.load(sys.stdin)
    except ValueError:
        data = None
    for field in fields:
        value = None
        for alternative in field.split("|"):
            value = dig(data, alternative)
            if value not in (None, ""):
                break
        print(render(value))


if __name__ == "__main__":
    main(sys.argv[1:])
