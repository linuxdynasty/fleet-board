# internal/agent/tmux

The agent's tmux control-mode code (01 E2). So far it holds the protocol
parser only; the `tmux -C` client that starts tmux and sends commands is a
later part of plan row 7.

- `NewParser(r, Options)` and `Parser.Next()` read a control client's output
  one message at a time: `Reply` (a `%begin` block and its `%end` or
  `%error`), `Output` (`%output` and `%extended-output`, decoded), `Pause`,
  `Continue`, `Exit`, `SubscriptionChanged`, `Unknown` (name only) and
  `Discarded` (an error class and a byte count).
- Inside a reply only the exact closing guard ends it, and only a well-formed
  `%pause %N` or `%continue %N` is lifted out; every other line is body,
  because command output can be pane text that starts with `%`.
- Bounds: `MaxLineBytes` (1 MiB) and `MaxReplyBytes` (8 MiB), in
  [defaults](../../../docs/specs/defaults.md). A longer line is skipped to its
  newline; memory never grows with a line's length.
- `NewMetrics(tel)` counts failures in `fleet_agent_tmux_parse_errors_total{err_kind}`
  and `fleet_agent_tmux_lines_too_long_total`, and logs `tmux.parse_error` and
  `tmux.line_too_long` at most once a minute per class (05b).

Tests: `CGO_ENABLED=0 go test ./internal/agent/tmux/`. The recorded
transcripts and how they were made are in [testdata](testdata/README.md).
`go test` runs the fuzz seeds (the transcripts and `testdata/fuzz/FuzzParse/`);
to fuzz: `go test -run='^$' -fuzz='^FuzzParse$' -fuzztime=60s ./internal/agent/tmux/`
(`FuzzOutputRoundTrip` the same way). Neither is in `make fuzz` yet.
