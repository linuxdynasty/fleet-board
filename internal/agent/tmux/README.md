# internal/agent/tmux

The agent's tmux control-mode code (01 E2). So far it holds the protocol
parser only; the `tmux -C` client that starts tmux and sends commands is a
later part of plan row 7.

- `NewParser(r, Options)` and `Parser.Next()` read a control client's output
  one message at a time: `Reply` (a `%begin` block and its `%end` or
  `%error`), `Output` (`%output` and `%extended-output`, decoded), `Pause`,
  `Continue`, `Exit`, `SubscriptionChanged`, `WindowAdd` (`%window-add`,
  `%unlinked-window-add`), `WindowClose` (`%unlinked-window-close`; the linked
  `%window-close` is not read yet), `WindowRenamed`, `SessionChanged`,
  `Unknown` (name only) and `Discarded` (an error class and a byte count).
  Window and session names are the developer's text: never logged.
- Inside a reply only the exact closing guard ends it and nothing is lifted
  out: command output can be pane text that starts with `%`. tmux writes
  `%pause %N` and `%continue %N` inside the reply to the client's own
  `refresh-client -A`; `Reply.ControlNotes()` finds those lines. The client
  (7c2) may honour a `%pause` from any reply (pausing is the safe direction),
  but a `%continue` only from its own `refresh-client -A` reply for that
  pane, never marking a pane resumed on a `%continue` it did not ask for; 7c2
  proves this with a transcript of a flood during a command.
- Bounds: `MaxLineBytes` (1 MiB) and `MaxReplyBytes` (8 MiB), in
  [defaults](../../../docs/specs/defaults.md). A longer line is skipped to its
  newline; memory never grows with a line's length. Each kept reply line is
  charged its bytes plus `ReplyLineOverhead` (17), so a reply of empty lines
  is bounded too. Options are per Parser; with the defaults a scrollback
  capture of more than about 86,000 full 80-column lines (8 MiB at 97 bytes
  a line) is cut short and flagged by `Reply.Dropped` (scrollback history
  goes through `LOGS` and `pipe-pane` per the spec, not a capture reply).
- `NewMetrics(tel)` counts failures in `fleet_agent_tmux_parse_errors_total{err_kind}`
  and `fleet_agent_tmux_lines_too_long_total`, and logs `tmux.parse_error` and
  `tmux.line_too_long` at most once a minute per class (05b).

Tests: `CGO_ENABLED=0 go test ./internal/agent/tmux/`. Tests that need a
real tmux use the throwaway servers of [`tmuxtest`](tmuxtest/README.md). The recorded
transcripts and how they were made are in [testdata](testdata/README.md).
`go test` runs the fuzz seeds (the transcripts and `testdata/fuzz/FuzzParse/`);
to fuzz: `go test -run='^$' -fuzz='^FuzzParse$' -fuzztime=60s ./internal/agent/tmux/`
(`FuzzOutputRoundTrip` the same way); `make fuzz` runs both.
