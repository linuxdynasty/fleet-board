#!/usr/bin/env python3
"""Validate recorded sample counts and reproduce summary.json without Go runs."""

import json
import pathlib
import statistics

ROOT = pathlib.Path(__file__).resolve().parent
NAMES = ("BenchmarkDeadlineCostSend", "BenchmarkDeadlineCostDecide")
UNITS = ("ns/op", "callback-ns/op", "rpc-ns/op", "B/op", "allocs/op")


def main() -> None:
    result = {}
    for arm in ("baseline", "candidate"):
        rows = {name: {unit: [] for unit in UNITS} for name in NAMES}
        for line in (ROOT / f"{arm}.bench").read_text().splitlines():
            if not line.startswith("Benchmark"):
                continue
            fields = line.split()
            name = fields[0].rsplit("-", 1)[0]
            if name not in rows or fields[1] != "5":
                raise ValueError(f"unexpected benchmark or call count: {line}")
            metrics = dict(zip(fields[3::2], map(float, fields[2::2])))
            if set(metrics) != set(UNITS):
                raise ValueError(f"unexpected metrics: {line}")
            for unit, value in metrics.items():
                rows[name][unit].append(value)
        result[arm] = {}
        for name, metrics in rows.items():
            result[arm][name] = {}
            for unit, values in metrics.items():
                if len(values) != 20:
                    raise ValueError(f"{arm} {name} {unit}: expected 20 samples")
                result[arm][name][unit] = {
                    "n": len(values), "median": statistics.median(values),
                    "min": min(values), "max": max(values),
                }
    (ROOT / "summary.json").write_text(json.dumps(result, indent=2) + "\n")
    print("Validated 20 five-call samples per arm and operation; summary.json written.")


if __name__ == "__main__":
    main()
