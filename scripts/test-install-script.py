#!/usr/bin/env python3
"""Test packaging/install.sh against a fake release server.

Serves archives, SHA256SUMS and unit files over local HTTP, then runs the
real script with HOME pointed at a temp folder:

- happy path: the binary and the unit land, the unit names the home folder;
- tampered archive: the script fails and installs nothing (no binary, no unit);
- archive missing from SHA256SUMS: the script fails and installs nothing.

Usage: python3 scripts/test-install-script.py
Exit 0 when every check passes. Needs only the standard library, curl, tar
and shasum (or sha256sum) for the script itself.
"""

from __future__ import annotations

import functools
import hashlib
import http.server
import io
import os
import stat
import subprocess
import sys
import tarfile
import tempfile
import threading
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
SCRIPT = ROOT / "packaging" / "install.sh"
VERSION = "v9.9.9-test"


def make_archive(body: bytes, name: str = "fleet-agent") -> bytes:
    buf = io.BytesIO()
    with tarfile.open(fileobj=buf, mode="w:gz") as tar:
        info = tarfile.TarInfo(name)
        info.size = len(body)
        info.mode = 0o755
        tar.addfile(info, io.BytesIO(body))
    return buf.getvalue()


def serve(docroot: Path):
    handler = functools.partial(http.server.SimpleHTTPRequestHandler, directory=str(docroot))
    server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), handler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    return server


def run_script(home: Path, base: str, *args: str) -> subprocess.CompletedProcess[str]:
    env = dict(os.environ)
    env["HOME"] = str(home)
    env["FLEET_RELEASE_BASE"] = base
    env["FLEET_UNIT_BASE"] = base
    return subprocess.run(
        ["sh", str(SCRIPT), *args], capture_output=True, text=True, env=env, timeout=60
    )


def layout(docroot: Path, archive: bytes) -> None:
    tagged = docroot / VERSION
    tagged.mkdir(parents=True)
    name = "fleet-agent-linux-amd64.tar.gz"
    (tagged / name).write_bytes(archive)
    digest = hashlib.sha256(archive).hexdigest()
    (tagged / "SHA256SUMS").write_text(f"{digest}  {name}\n")
    launchd = docroot / "launchd"
    launchd.mkdir(exist_ok=True)
    (launchd / "com.fleet-board.fleet-agent.plist").write_text("<plist>@HOME@</plist>")
    systemd = docroot / "systemd"
    systemd.mkdir(exist_ok=True)
    (systemd / "fleet-agent.service").write_text("[Service] home=@HOME@\n")


def check_happy() -> list[str]:
    problems: list[str] = []
    with tempfile.TemporaryDirectory() as tmp:
        docroot = Path(tmp) / "htdocs"
        layout(docroot, make_archive(b"agent-binary"))
        server = serve(docroot)
        try:
            home = Path(tmp) / "home"
            home.mkdir()
            base = f"http://127.0.0.1:{server.server_port}"
            proc = run_script(
                home, base, "--program", "fleet-agent", "--version", VERSION,
                "--os", "linux", "--arch", "amd64", "--no-start",
            )
            if proc.returncode != 0:
                problems.append(f"happy path exited {proc.returncode}: {proc.stderr}")
                return problems
            binary = home / ".local" / "bin" / "fleet-agent"
            if not binary.is_file() or binary.read_bytes() != b"agent-binary":
                problems.append("happy path: the binary is missing or wrong")
            elif not (binary.stat().st_mode & stat.S_IXUSR):
                problems.append("happy path: the binary is not executable")
            unit = home / ".config" / "systemd" / "user" / "fleet-agent.service"
            if not unit.is_file():
                problems.append("happy path: the unit is missing")
            elif "@HOME@" in unit.read_text() or str(home) not in unit.read_text():
                problems.append("happy path: the unit still holds the placeholder")
        finally:
            server.shutdown()
    return problems


def check_tampered() -> list[str]:
    problems: list[str] = []
    with tempfile.TemporaryDirectory() as tmp:
        docroot = Path(tmp) / "htdocs"
        layout(docroot, make_archive(b"agent-binary"))
        # Tamper after the checksum file was written.
        tagged = docroot / VERSION
        (tagged / "fleet-agent-linux-amd64.tar.gz").write_bytes(make_archive(b"evil-binary"))
        server = serve(docroot)
        try:
            home = Path(tmp) / "home"
            home.mkdir()
            base = f"http://127.0.0.1:{server.server_port}"
            proc = run_script(
                home, base, "--program", "fleet-agent", "--version", VERSION,
                "--os", "linux", "--arch", "amd64", "--no-start",
            )
            if proc.returncode == 0:
                problems.append("tampered archive: the script succeeded, want a failure")
            if "checksum" not in proc.stderr.lower() and "checksum" not in proc.stdout.lower():
                problems.append("tampered archive: no checksum message on output")
            if (home / ".local" / "bin" / "fleet-agent").exists():
                problems.append("tampered archive: the binary was installed anyway")
            if (home / ".config" / "systemd" / "user" / "fleet-agent.service").exists():
                problems.append("tampered archive: the unit was installed anyway")
        finally:
            server.shutdown()
    return problems


def check_unlisted() -> list[str]:
    problems: list[str] = []
    with tempfile.TemporaryDirectory() as tmp:
        docroot = Path(tmp) / "htdocs"
        layout(docroot, make_archive(b"agent-binary"))
        tagged = docroot / VERSION
        (tagged / "SHA256SUMS").write_text("0" * 64 + "  something-else.tar.gz\n")
        server = serve(docroot)
        try:
            home = Path(tmp) / "home"
            home.mkdir()
            base = f"http://127.0.0.1:{server.server_port}"
            proc = run_script(
                home, base, "--program", "fleet-agent", "--version", VERSION,
                "--os", "linux", "--arch", "amd64", "--no-start",
            )
            if proc.returncode == 0:
                problems.append("unlisted archive: the script succeeded, want a failure")
            if (home / ".local" / "bin" / "fleet-agent").exists():
                problems.append("unlisted archive: the binary was installed anyway")
        finally:
            server.shutdown()
    return problems


def main() -> int:
    if not SCRIPT.is_file():
        print("test-install-script: packaging/install.sh is missing")
        return 1
    problems = check_happy() + check_tampered() + check_unlisted()
    if problems:
        print("test-install-script: failures:")
        for problem in problems:
            print(f"  {problem}")
        return 1
    print("test-install-script: install, tamper refusal and unlisted refusal pass.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
