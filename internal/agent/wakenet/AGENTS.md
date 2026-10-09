# internal/agent/wakenet

Read the root `AGENTS.md` first; it applies here. Load `fleet-go` and
`fleet-telemetry-coverage`; `fleet-security` for anything that reads kernel
messages. Rules for this package:

- ADR 0029 is the rule. Never `os/exec` (nor `os.StartProcess`,
  `syscall.Exec`, `syscall.ForkExec`, `unix.Exec`), never cgo, never
  `  kern.waketime`, never `CLOCK_MONOTONIC` in a file built for darwin, never
  `x/net/route`, `godbus/dbus` or `purego`. `TestForbiddenSources` enforces
  this.
- A routing push message is only a hint. Nothing reconnects unless a fresh
  reading differs from the last one. There is no always-on poll; the 15 s poll
  runs only while the push socket is off. A failed read with the push socket
  on is retried (2 s, then every 15 s) until one works or a new hint arrives.
- A failure that repeats is logged once, then once on recovery with the count.
- Parsers are pure byte code with no build tag, so their tests run on any OS.
  Every length is checked before it is used; a malformed buffer never panics
  (`FuzzNetlink`, `FuzzRouteSocket`). A new kernel constant gets a line in
  `consts_darwin_test.go` or `consts_linux_test.go`.
- Every clock, reader and socket is a struct field; tests use the fakes in
  `fakes_test.go` and never sleep. A loop test waits on a timer request or a
  log line, never on wall time. The one exception is
  `TestPushSocketCloseUnblocksRead`: a real socket cannot signal that Read is
  parked.
- Logs carry interface names and counts, never IP addresses.
- A new log event name goes into 03's W1 Telemetry list in the same change
  (`make telemetry-names`).
- A change here updates this README and `doc.go` in the same commit.
