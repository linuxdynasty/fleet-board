# internal/agent/tmux/tmuxtest

The test rig for tmux (plan row 7, PR 7c2): the only way a test starts tmux,
so no test can reach the developer's own server.

```go
func TestMain(m *testing.M) { os.Exit(tmuxtest.Main(m)) } // TMUX unset, TMUX_TMPDIR empty

for _, b := range tmuxtest.Binaries() {               // "3.2" and "latest"
	t.Run(b.Name, func(t *testing.T) {
		srv := tmuxtest.Start(t, b, "-s", "work", "exec cat") // new-session -d ...
		srv.Run(t, "rename-window", "-t", "@0", "x")          // one-shot, through internal/proc
		argv, env := srv.Argv("attach-session", "-t", "work"), srv.Env()
	})
}
```

- `Start` makes a short temp folder (`os.MkdirTemp` in the system temp
  folder, or `/tmp` when that is too long; `t.TempDir` is too long for a
  macOS socket path, 104 bytes), uses `-S <folder>/s` and `-f /dev/null`,
  and runs tmux with the test's environment minus `TMUX`, plus
  `TMUX_TMPDIR=<folder>` and `TERM=xterm-256color`. Cleanup runs
  `kill-server` and fails the test if `has-session` still finds the server,
  then removes the folder.
- `Kill` then `NewSession` starts a new server on the same socket (a
  restart).
- `Sibling(tb, name, args...)` starts another server in the same folder
  whose socket is `name` (the inventory package's socket-folder watch needs
  a `default` socket there); it is killed and checked gone like the first,
  and `Sweep` kills every socket in an orphaned folder.
- `Run` and `Try` run under the measured `tmux` caller of `internal/proc`
  (5 s, 16 KiB), so a reply up to 16 KiB comes back whole.
- Each server folder holds an `owner` file: the pid of the test process that
  started the server, and nothing else. A test binary that dies before its
  cleanup (a panic, a kill) leaves its server running; `Sweep`, which `Main`
  runs first, kills the servers whose recorded pid is gone and removes their
  folders. It trusts a folder only when the `fbt*` entry (in the system temp
  folder, `$TMPDIR` on macOS, and `/tmp`) and its `owner` file are a real
  folder and a regular file, not symlinks, owned by this user, with no group
  or other permission bits; it reads only the pid, runs `kill-server` with
  the tmux this package found itself (`Binaries`), never a program named on
  disk, and never matches a tmux process by name. By hand:
  `<tmux> -S <folder>/s kill-server; rm -rf <folder>` for a folder whose
  `owner` pid is not running (`kill -0 <pid>` fails).
- Caveat: a pid means something only inside one pid namespace. Test
  processes in containers that share `/tmp` with the host (or with each
  other) can see a live test's pid as dead and sweep its server; do not
  share the temp folder between pid namespaces while tests run.
- CI runs the real-tmux tests only for `./internal/agent/tmux/...` (the
  "tmux tests" step of `ci.yml`); a package outside it that starts using
  this rig must be added to that step, or its tests skip in CI.
- `Binaries`: `FLEET_TMUX_32` is a tmux 3.2 build (CI: 3.2a);
  `FLEET_TMUX_LATEST` the latest release, else the tmux on `PATH`. A missing
  one skips its subtest with a message; `FLEET_REQUIRE_TMUX=1` (CI) fails
  it instead. CI builds both with `scripts/install-tmux.sh` from the pins in
  `tool-versions.env`.
- One-shot commands go through `internal/proc` (caller `tmux`), so this
  package imports no `os/exec` (the depguard rule allows it only in the
  `internal/agent/tmux` package's own files). A long-lived client, such as
  the PTY "person at the keyboard", is started by the test file with
  `Argv` and `Env` (test files may start processes).

Tests: `CGO_ENABLED=0 go test ./internal/agent/tmux/tmuxtest/`.
