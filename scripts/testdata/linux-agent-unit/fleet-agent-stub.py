#!/usr/bin/python3
"""Disposable service fixture: a tmux pane and a short-lived helper."""

import os
import signal
import subprocess
import sys
import time


socket = os.path.join(os.environ["XDG_RUNTIME_DIR"], "fleet44-unit-test.sock")
if not os.path.exists(socket):
    subprocess.run(
        [
            "tmux", "-S", socket, "-f", "/dev/null", "new-session", "-d",
            "-s", "fleet44-test", "sh -c 'while :; do date +%s; sleep 1; done'",
        ],
        check=True,
    )

helper = subprocess.Popen(["sleep", "3600"])


def stop(_signal, _frame):
    helper.terminate()
    helper.wait(timeout=5)
    sys.exit(0)


signal.signal(signal.SIGTERM, stop)
while True:
    time.sleep(1)
