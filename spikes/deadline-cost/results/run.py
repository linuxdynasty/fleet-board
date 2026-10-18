#!/usr/bin/env python3
"""Run the precompiled deadline-cost binaries during an exclusive host window."""

import os
import pathlib
import subprocess
import sys
import time


ROOT = pathlib.Path(__file__).resolve().parents[3]
RESULTS = pathlib.Path(__file__).resolve().parent
TEMP = pathlib.Path("/tmp")
BENCH = "^BenchmarkDeadlineCost(Send|Decide)$"


def command(label: str, arm: str, args: list[str]) -> None:
    binary = TEMP / f"fleet-deadline-cost-{arm}" / "api.test"
    if not binary.is_file():
        raise SystemExit(f"missing precompiled {binary}")
    env = os.environ.copy()
    env["FLEET_DEADLINE_ARM"] = arm
    argv = [str(binary), "-test.run=^$", f"-test.bench={BENCH}", "-test.benchmem"] + args
    started = time.monotonic()
    result = subprocess.run(argv, cwd=ROOT, env=env, text=True, capture_output=True)
    elapsed = time.monotonic() - started
    (RESULTS / f"{label}.bench").write_text(result.stdout + result.stderr)
    with (RESULTS / "commands-exits.txt").open("a") as log:
        log.write(f"{label}: {' '.join(argv)}\nexit={result.returncode} wall_s={elapsed:.3f}\n")
    print(f"{label}: exit={result.returncode} wall_s={elapsed:.1f}", flush=True)
    if result.returncode:
        raise SystemExit(result.returncode)


def profile(arm: str) -> None:
    directory = TEMP / f"fleet-deadline-cost-{arm}"
    command(
        f"{arm}-profile", arm,
        [
            "-test.benchtime=20x", "-test.count=1",
            f"-test.cpuprofile={directory / 'cpu.prof'}",
            f"-test.memprofile={directory / 'heap.prof'}",
            f"-test.mutexprofile={directory / 'mutex.prof'}",
            "-test.mutexprofilefraction=1",
        ],
    )
    for kind, extra in (
        ("cpu", []),
        ("heap", ["-alloc_space"]),
        ("mutex", ["-sample_index=delay"]),
    ):
        argv = ["go", "tool", "pprof", "-top", "-nodecount=25", *extra,
                str(directory / "api.test"), str(directory / f"{kind}.prof")]
        result = subprocess.run(argv, cwd=ROOT, text=True, capture_output=True)
        (RESULTS / f"{arm}-{kind}-top.txt").write_text(result.stdout + result.stderr)
        with (RESULTS / "commands-exits.txt").open("a") as log:
            log.write(f"{' '.join(argv)}\nexit={result.returncode}\n")
        if result.returncode:
            raise SystemExit(result.returncode)


def benchstat() -> None:
    for arm in ("baseline", "candidate"):
        rows = []
        for order in ("normal", "reverse"):
            for line in (RESULTS / f"{order}-{arm}.bench").read_text().splitlines():
                if line.startswith("BenchmarkDeadlineCost"):
                    rows.append(line)
        if len(rows) != 40:
            raise SystemExit(f"{arm}: got {len(rows)} benchmark rows, want 40")
        (RESULTS / f"{arm}.bench").write_text("\n".join(rows) + "\n")
    tool = pathlib.Path.home() / "go/bin/benchstat"
    argv = [str(tool), str(RESULTS / "baseline.bench"), str(RESULTS / "candidate.bench")]
    result = subprocess.run(argv, cwd=ROOT, text=True, capture_output=True)
    (RESULTS / "benchstat.txt").write_text(result.stdout + result.stderr)
    with (RESULTS / "commands-exits.txt").open("a") as log:
        log.write(f"{' '.join(argv)}\nexit={result.returncode}\n")
    if result.returncode:
        raise SystemExit(result.returncode)


def main() -> None:
    if sys.argv[1:] != ["--quiet-released"]:
        raise SystemExit("timed run requires coordinator's explicit quiet release")
    (RESULTS / "commands-exits.txt").write_text(
        "Deadline-cost run: fresh processes, five calls/sample, 10 normal and "
        "10 reverse samples per arm.\n"
    )
    for arm in ("baseline", "candidate"):
        command(f"cold-process-{arm}", arm, ["-test.benchtime=5x", "-test.count=1"])
    for order, arms in (("normal", ("baseline", "candidate")),
                        ("reverse", ("candidate", "baseline"))):
        for arm in arms:
            command(f"{order}-{arm}", arm, ["-test.benchtime=5x", "-test.count=10"])
    for arm in ("baseline", "candidate"):
        profile(arm)
    benchstat()


if __name__ == "__main__":
    main()
