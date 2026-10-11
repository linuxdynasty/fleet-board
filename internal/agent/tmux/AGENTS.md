# internal/agent/tmux

Read the root `AGENTS.md` first; it applies here. Load `fleet-go`,
`fleet-tmux`, `fleet-security` and `fleet-telemetry-coverage`. Rules for this
package:

- The parser starts no process and writes nothing. It reads a stream; only
  `conn.go` uses `os/exec`, for the long-lived `tmux -C` client (the depguard
  rule allows `internal/agent/tmux/*.go`; keep it to that one file). One-shot
  tmux calls go through `internal/proc` under the `tmux` caller.
- The client takes no command string: each command is a typed method, and
  `Send` takes a `typing.Line` only, refused for another server or start.
  `ClientOptions` has no default socket.
- Tests start tmux only through `tmuxtest` (a throwaway server, `-S` in a
  short temp folder, `-f /dev/null`, killed and checked gone), and every
  test binary here has `TestMain` call `tmuxtest.Main`. Never the
  developer's server. No test waits for a `%pause` (spike 0.5 finding 7).
- A change to the attach loop, the reply queue or the locks updates the
  concurrency diagram in the README in the same commit.
- Everything a pane or a reply holds is untrusted data. It never reaches a
  log line, a span or a metric label: error classes and byte counts only.
- Inside a reply only the exact closing guard ends it and nothing is lifted
  out: reply lines can be pane text. A `%pause` line in any reply
  (`Reply.ControlNotes`) is safe to honour (pausing is the safe direction,
  and tmux may pause a pane while another command runs); a `%continue` line
  is honoured only when the reply answers the client's own
  `refresh-client -A` for that pane, so a pane is never marked resumed on a
  `%continue` the client did not ask for (`TestPauseFromAnyReplyContinueOnlyFromOwnRequest`;
  the flood transcripts in `testdata/` show tmux keeps a reply whole).
- Every line and every reply is bounded, and the reply bound charges each
  kept line its overhead; a new field that keeps bytes needs a bound and a
  test that a huge or many-small input does not grow memory.
- A new tmux version gets its own recorded transcript in `testdata/`, made on
  a throwaway server as `testdata/README.md` says, never the developer's.
- A change here updates this README, `doc.go`, 01 E2 and the 05b rows in the
  same commit.
