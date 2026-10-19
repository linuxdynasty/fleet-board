#!/usr/bin/env python3
"""Build reproducible, temporary Go overlays for the deadline cost probe."""

import json
import pathlib
import subprocess
import sys


ROOT = pathlib.Path(__file__).resolve().parents[3]
API = pathlib.Path("internal/hub/api")
# API tree objects survive a rebase and are included in this branch history.
BASELINE = "dbd18ef4b022648a45f7e82c7221e68640fd00a9"
CANDIDATE = "515526b8925cab1dc1a647473ceee418b880d399"
HISTORICAL_STORE = "6ac9e9b662aae4993831dbb9b2cbd966d8c6ceed"


def source(rev: str, name: str) -> str:
    return subprocess.check_output(
        ["git", "show", f"{rev}:{name}"], cwd=ROOT, text=True
    )


def once(body: str, old: str, new: str) -> str:
    if body.count(old) != 1:
        raise ValueError(f"wanted one occurrence of {old!r}; got {body.count(old)}")
    return body.replace(old, new, 1)


def session_body(rev: str) -> str:
    body = source(rev, "session.go")
    body = once(
        body,
        "const approvalTimeout = 30 * time.Minute",
        "const approvalTimeout = 30 * time.Minute\n\n"
        "// DeadlineCostRecord is used only by the temporary benchmark overlay.\n"
        "var DeadlineCostRecord = func(string, time.Duration) {}",
    )
    return once(
        body,
        "err = f.store.Write(ctx, func(tx *sql.Tx) error {\n",
        "err = f.store.Write(ctx, func(tx *sql.Tx) error {\n"
        "\t\tdeadlineCostStart := time.Now()\n"
        "\t\tdefer func() { DeadlineCostRecord(\"send\", time.Since(deadlineCostStart)) }()\n",
    )


def approval_body() -> str:
    body = source(CANDIDATE, "approvals_rpc.go")
    prefix, tail = body.split("func (f *Fleet) DecideApproval(", 1)
    method, suffix = tail.split("func (f *Fleet) decideStoreError(", 1)
    method = once(
        method,
        "err = f.store.Write(ctx, func(tx *sql.Tx) error {\n",
        "err = f.store.Write(ctx, func(tx *sql.Tx) error {\n"
        "\t\tdeadlineCostStart := time.Now()\n"
        "\t\tdefer func() { DeadlineCostRecord(\"decide\", time.Since(deadlineCostStart)) }()\n",
    )
    return (
        prefix + "func (f *Fleet) DecideApproval(" + method
        + "func (f *Fleet) decideStoreError(" + suffix
    )


def test_helper(name: str, function: str) -> str:
    body = source(CANDIDATE, name)
    return once(body, f"func {function}(t *testing.T,", f"func {function}(t testing.TB,")


def main() -> None:
    if len(sys.argv) != 3 or sys.argv[1] not in ("baseline", "candidate"):
        raise SystemExit("usage: prepare.py baseline|candidate TEMP_DIR")
    arm = sys.argv[1]
    target = pathlib.Path(sys.argv[2]).resolve()
    target.mkdir(parents=True, exist_ok=True)
    rev = BASELINE if arm == "baseline" else CANDIDATE
    replacements = {
        "session.go": session_body(rev),
        "session_create.go": source(rev, "session_create.go"),
        "approvals_rpc.go": approval_body(),
        "fleet_helpers_test.go": test_helper("fleet_helpers_test.go", "newFleetEnv"),
        "local_quota_accounts_test.go": test_helper(
            "local_quota_accounts_test.go", "newLocalQuotaEnv"
        ),
        "session_test.go": source(CANDIDATE, "session_test.go"),
        "session_deadline_test.go": (
            ROOT / "spikes/deadline-cost/results/deadline_probe_test.go.txt"
        ).read_text(),
    }
    replacements["session_test.go"] = once(
        replacements["session_test.go"],
        "func enrollMachine(t *testing.T,",
        "func enrollMachine(t testing.TB,",
    )
    replacements["session_test.go"] = once(
        replacements["session_test.go"],
        "func createSession(t *testing.T,",
        "func createSession(t testing.TB,",
    )
    overlay = {}
    for name, body in replacements.items():
        actual = ROOT / API / name
        replacement = target / name
        replacement.write_text(body)
        overlay[str(actual)] = str(replacement)
    # The measurements predate CommitWake. Reproduce that historical store
    # while using historical API tree objects; this is not a new measurement.
    for name in ("store.go", "writer.go", "doc.go", "commit.go"):
        relative = pathlib.Path("internal/store") / name
        body = "package store\n" if name == "commit.go" else subprocess.check_output(
            ["git", "show", f"{HISTORICAL_STORE}:{relative}"], cwd=ROOT, text=True
        )
        replacement = target / f"store-{name}"
        replacement.write_text(body)
        overlay[str(ROOT / relative)] = str(replacement)
    (target / "overlay.json").write_text(json.dumps({"Replace": overlay}, indent=2))
    print(target / "overlay.json")


if __name__ == "__main__":
    main()
