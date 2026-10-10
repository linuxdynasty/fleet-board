# internal/proc

Read the root `AGENTS.md` first; it applies here. Load `fleet-go`,
`fleet-security` and `fleet-telemetry-coverage`. Rules for this package:

- This is the one place a one-shot child process starts (01 principle 8).
  Standard library plus `internal/telemetry` and the OpenTelemetry metric API
  only; no cgo; nothing from `internal/agent` or `internal/hub`, since both
  programs import it. No package state besides the caller table.
- `Run` must keep all of these, each with a test that fails without it: empty
  stdin, the time cap, the output cap (stdout and stderr together, memory
  bounded by the cap), a process group of its own, the group killed on a cap,
  on cancel and after every run, `WaitDelay` set, and a prompt return. Break
  one on purpose and see the test fail before you change the code around it.
- There is no default cap. A caller exists only as a row of the defaults row
  "One-shot child process" and an entry in `limits.go`, and
  `TestCallersMatchTheDefaultsRow` keeps them equal. A new caller adds both in
  one commit, with numbers measured from its real call.
- `CallerForTest` (`export_test.go`) is for this package's tests. Do not add
  a way to build a `Caller` with caps of your own to non-test code.
- A signal carries the caller name, the cause, the pid and counts. Never the
  program, the arguments, the environment, the directory or the output: they
  can hold secrets or user text. The `cause` and `caller` label sets are
  closed.
- The helper reads no environment variable and adds none, and opens no
  credential. `Env` is the caller's choice.
- A `//nolint:gosec` at the exec site is the only one allowed in non-test
  code and says why. Test children are this test binary started again; use no
  external tool.
- Run `go test -race -count=20 ./internal/proc/` after any change to the
  kill or wait logic: the tests poll for a process to be gone (a zombie
  answers signal 0 until it is reaped), and a flake is a bug.
- A change here updates this README and `doc.go` in the same commit, and the
  rules it describes in `.golangci.yml` and `scripts/test-lint-exec-rules.sh`.
