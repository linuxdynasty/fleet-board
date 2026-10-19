#!/usr/bin/env python3
"""Exercise an enrolled agent and its real tmux child in a disposable Linux VM."""

import os
import pathlib
import re
import socket as net
import subprocess
import sys
import time


UNIT = "fleet-agent.service"
SOCKET = pathlib.Path(os.environ.get("FLEET44_TEST_SOCKET", ""))


def run(*args):
    result = subprocess.run(args, text=True, capture_output=True, timeout=20, check=False)
    if result.returncode:
        raise AssertionError(f"{' '.join(args)} exited {result.returncode}")
    return result.stdout.strip()


def wait_for(label, read, accepted, timeout=15):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        value = read()
        if accepted(value):
            return value
        time.sleep(0.2)
    raise AssertionError(f"timed out waiting for {label}")


def property_value(name):
    return run("systemctl", "--user", "show", UNIT, "--property=" + name, "--value")


def group_pids():
    group = property_value("ControlGroup")
    assert group.startswith("/user.slice/") and group.endswith("/fleet-agent.service"), group
    return [int(pid) for pid in pathlib.Path("/sys/fs/cgroup" + group, "cgroup.procs").read_text().split()]


def observer_pid():
    for pid in group_pids():
        try:
            args = pathlib.Path(f"/proc/{pid}/cmdline").read_bytes().split(b"\0")
        except FileNotFoundError:
            continue
        if b"-C" in args and b"-S" in args and os.fsencode(SOCKET) in args:
            return pid
    return None


def pane_pid():
    value = run("tmux", "-S", str(SOCKET), "list-panes", "-F", "#{pane_pid} #{pane_dead}")
    lines = value.splitlines()
    assert len(lines) == 1 and lines[0].endswith(" 0"), value
    return int(lines[0].split()[0])


def last_output():
    output = run("tmux", "-S", str(SOCKET), "capture-pane", "-p", "-S", "-20")
    values = [int(line) for line in output.splitlines() if re.fullmatch(r"\d+", line)]
    return values[-1] if values else 0


def progressing_output():
    before = last_output()
    return wait_for("pane output", last_output, lambda value: value > before, timeout=5)


def active_agent_and_child(previous_main=0, previous_child=0):
    main = wait_for("active agent", lambda: int(property_value("MainPID") or "0"),
                    lambda value: value > 0 and value != previous_main)
    child = wait_for("tmux -C child", observer_pid,
                     lambda value: value is not None and value != previous_child)
    assert os.path.samefile(f"/proc/{main}/exe", pathlib.Path.home() / ".local/bin/fleet-agent"), (
        "service main is not the built fleet-agent")
    assert os.path.samefile(f"/proc/{child}/exe", "/usr/bin/tmux"), (
        "observer child is not the installed tmux")
    parent = next(line.split()[1] for line in pathlib.Path(f"/proc/{child}/status").read_text().splitlines()
                  if line.startswith("PPid:"))
    assert int(parent) == main, "tmux -C is not a direct agent child"
    assert main in group_pids() and child in group_pids()
    assert pathlib.Path(f"/proc/{main}/cgroup").read_text() == pathlib.Path(
        f"/proc/{child}/cgroup").read_text()
    return main, child


def stopped_child(main, child, pane):
    run("systemctl", "--user", "stop", UNIT)
    wait_for("inactive service", lambda: property_value("ActiveState"), lambda value: value == "inactive")
    assert not pathlib.Path(f"/proc/{main}").exists(), "old main still exists"
    assert not pathlib.Path(f"/proc/{child}").exists(), "tmux -C child still exists"
    assert pane_pid() == pane, "pane changed after stop"
    progressing_output()


def only_one_writer():
    result = subprocess.run(
        [str(pathlib.Path.home() / ".local/bin/fleet-agent"), "run"],
        text=True, capture_output=True, timeout=10, check=False,
    )
    assert result.returncode == 1 and "fleet-agent.lock" in result.stderr, "second writer was not refused"
    assert property_value("ActiveState") == "active"


def main():
    if net.gethostname() != "fleet44unit" or os.environ.get("FLEET44_DISPOSABLE_VM") != "yes":
        raise SystemExit("requires the named disposable fleet44unit VM")
    if not str(SOCKET).startswith(os.environ.get("XDG_RUNTIME_DIR", "\0") + "/fleet44-"):
        raise SystemExit("test socket must be inside the VM user runtime folder")
    assert SOCKET.is_socket(), "the test tmux server must already exist"
    assert property_value("ActiveState") == "active", "enrolled agent service must be active"
    pane = pane_pid()
    first_main, first_child = active_agent_and_child()
    progressing_output()
    stopped_child(first_main, first_child, pane)
    assert property_value("NRestarts") == "0", "manual stop caused a restart"
    run("systemctl", "--user", "reset-failed", UNIT)
    run("systemctl", "--user", "start", UNIT)
    second_main, second_child = active_agent_and_child(first_main, first_child)
    assert pane_pid() == pane
    progressing_output()
    only_one_writer()
    run("systemctl", "--user", "restart", UNIT)
    third_main, third_child = active_agent_and_child(second_main, second_child)
    assert not pathlib.Path(f"/proc/{second_child}").exists()
    assert pane_pid() == pane
    progressing_output()
    stopped_child(third_main, third_child, pane)
    print(f"PASS real observer cleanup: pane={pane}, mains={first_main},{second_main},{third_main}, "
          f"children={first_child},{second_child},{third_child}")


if __name__ == "__main__":
    try:
        main()
    except (AssertionError, OSError, subprocess.TimeoutExpired) as error:
        print(f"FAIL real observer cleanup: {error}", file=sys.stderr)
        raise SystemExit(1) from error
