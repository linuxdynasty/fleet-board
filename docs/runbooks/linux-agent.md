# Run the agent as a Linux user service

The Linux worker runs `fleet-agent` under the owner's `systemd --user` manager.
The unit template is [fleet-agent.service](../../packaging/systemd/fleet-agent.service).
It starts the agent only when the owner enables it. Enrollment is a separate,
explicit step; the unit does not enroll or create an identity.

Install the verified `fleet-agent` binary at `~/.local/bin/fleet-agent` and the
unit at `~/.config/systemd/user/fleet-agent.service`, both as the worker's normal
user. Release verification and the installer are separate #44 work. Do not
start an unverified binary. Then run the enrollment command issued by the hub
as that same user. The owner can inspect the local checks with
`fleet-agent doctor` before enabling the unit:

```sh
systemctl --user daemon-reload
systemctl --user enable --now fleet-agent.service
systemctl --user status fleet-agent.service
journalctl --user -u fleet-agent.service -b
```

The owner runs `sudo loginctl enable-linger <user>` once on each Linux worker.
That allows the user manager to run after logout. Check it with
`loginctl show-user <user> --property=Linger`; `fleet-agent doctor` also reports
whether it is on. The installer does not turn lingering on. A service that
starts before enrollment exits with code 1 and reaches the bounded start limit.
After enrollment, use `systemctl --user reset-failed fleet-agent.service` and
start it. A bad command line exits with code 2 and does not auto-restart.

The unit inherits the user manager's environment. It does not run a login shell
or read a profile. If a harness lives outside that manager's `PATH`, put a
single persistent drop-in at
`~/.config/systemd/user/fleet-agent.service.d/10-path.conf`. For example, with
the actual installed paths for this user:

```ini
[Service]
Environment="PATH=/home/alice/.local/bin:/opt/harness/bin:/usr/local/bin:/usr/bin"
```

Then run `systemctl --user daemon-reload` and restart the agent. Keep this
drop-in limited to `PATH`; do not copy credentials or the whole shell
environment into it. A non-default state folder or config path needs a
separate owner-written `ExecStart=` drop-in with explicit, quoted arguments:

```ini
[Service]
ExecStart=
ExecStart="/home/alice/.local/bin/fleet-agent" --state-dir "/home/alice/fleet state" run
```

Replace the example paths with absolute paths for that user. `ExecStart` is an
argument list parsed by systemd, not a shell command. Check the resulting unit
with `systemd-analyze --user verify` and
`systemctl --user cat fleet-agent.service` before restarting it.

`systemctl --user stop fleet-agent.service` is a manual stop and stays stopped.
Ordinary hub loss is handled by the agent's reconnect loop, so it does not
restart the service. A failed agent process restarts after five seconds, at most
five starts in 60 seconds. The unit sends SIGTERM to the agent and gives it 30
seconds to stop. `KillMode=process` leaves tmux and its panes running when the
service stops or restarts. This is deliberate: killing the whole service group
could kill the developer's sessions. It also means a hard-killed agent may
leave a transient child process; inspect it separately and never kill the
owner's tmux sessions as cleanup. The agent's state-folder lock admits only one
writer when it starts again. Inspect `journalctl --user -u fleet-agent.service`
and `fleet-agent doctor` if it cannot start.

The unit's source test is `python3 scripts/test-linux-agent-unit.py`. It checks
the template and the numeric defaults. On Linux it also runs
`systemd-analyze --user verify` when a binary is present at the unit's path.
Without that binary, the parser check is skipped and reported as such.

## Isolated Linux check, 2026-10-19

An isolated Ubuntu 24.04 arm64 VM ran a real `systemd --user` manager with
lingering on. `systemd-analyze --user verify` accepted the shipped unit (exit
0). Before enrollment, a built `fleet-agent` exited 1 five times at the
five-second delay; systemd then reported `Start request repeated too quickly`
under the 60-second, five-start limit. After enrollment against a throwaway
local hub, the service stayed active. A second `fleet-agent run` exited 1 with
the state-folder lock refusal, while the service's main process stayed active.
Manual stop left the real service inactive after seven seconds. Starting it
again gave a new main process. A test program exiting 2 had zero restarts.

A separate test program started tmux and a helper from inside this unit. The
tmux server and helper shared the service's control group. Ubuntu's tmux moved
the pane into its own `tmux-spawn` scope. The server stayed in the service group,
and the pane kept its process ID and continued printing after stop and restart.
The program cleaned up its helper on SIGTERM; the helper was gone after both
stop and restart. This checks the unit's kill behavior. The
test program is [fleet-agent-stub.py](../../scripts/testdata/linux-agent-unit/fleet-agent-stub.py).

A follow-up used the built, enrolled agent and an isolated tmux socket folder
named in a VM-only `TMUX_TMPDIR` service drop-in. Its real `tmux -C` observer
was a direct child of the agent in the same service control group. The
[real-child test](../../scripts/test-linux-agent-real-child.py) checked the
executable paths and process IDs, then stopped and restarted the service. Each
old observer process was gone after stop or restart; a new observer appeared
after start. The separate tmux server and pane kept running and the pane output
advanced throughout. A second real agent was refused by the state-folder lock.
The test resets systemd's start counter between quick test restarts; the start
limit was checked separately above. To repeat this check, use a disposable VM
named `fleet44unit` with an enrolled agent. Create a test-only socket and pane
as that VM user, then set `TMUX_TMPDIR` only in a test drop-in:

```sh
test_root="$XDG_RUNTIME_DIR/fleet44-real-tmux"
test_socket="$test_root/tmux-$(id -u)/default"
mkdir -p "$test_root/tmux-$(id -u)" ~/.config/systemd/user/fleet-agent.service.d
tmux -S "$test_socket" new-session -d -s fleet44-proof \
  'while :; do date +%s; sleep 1; done'
printf '[Service]\nEnvironment="TMUX_TMPDIR=%s"\n' "$test_root" \
  > ~/.config/systemd/user/fleet-agent.service.d/90-fleet44-tmux.conf
systemctl --user daemon-reload
systemctl --user reset-failed fleet-agent.service
systemctl --user start fleet-agent.service
```

Once the enrolled service is active, run:

```sh
FLEET44_DISPOSABLE_VM=yes \
FLEET44_TEST_SOCKET="$XDG_RUNTIME_DIR/fleet44-real-tmux/tmux-$(id -u)/default" \
python3 scripts/test-linux-agent-real-child.py
```

The test ends with the agent stopped and the pane running. Remove the test
drop-in and socket only from that disposable VM after reviewing the proof.

Other transient child kinds and a hard-killed agent's cleanup remain untested.

Logout followed by an ONLINE reading and the four named machine checks remain
**NOT_CHECKED**. The whole #44 install and upgrade flow also remains
**NOT_CHECKED**.
