# internal/agent/tmux/typing

Read the root `AGENTS.md` first; it applies here. Load `fleet-go`,
`fleet-tmux` and `fleet-security`; add `fleet-telemetry-coverage` for a
change to the refusal metric or log. Rules for this package:

- This is the only place a `send-keys` line is built (product principle 4,
  #140). Never add a way to send a raw tmux command, and never add a
  constructor that takes one.
- Every exported package-level function is named `New…`; a constructor that
  returns a `Line` takes a whole `PaneKey` and a `Purpose` (row-7 ruling,
  fork 11); never a bare pane id. No exported var or const
  of type `Line` (or a type holding one), no exported var with an inferred
  type, no exported method that returns a `Line`. `surface_test.go` enforces
  all of it; keep its RED cases next to any new rule.
- `Line` fields stay unexported, and the zero `Line` types nothing:
  `Command` returns `ErrZeroLine`. Keep `IsZero` exact.
- Refuse, never repair. Text with a newline, carriage return or other control
  character, over 4 KiB, or not UTF-8 is refused whole; nothing is escaped,
  stripped or cut. A target is a known server, a `[a-z0-9-]` start id and a
  tmux pane id (`%<digits>`); the `Line` keeps all three for the client.
- The `Purpose` set is the ruling's: the four `SendKeysPurpose` values by
  name and number (`TestSendKeysPurposesMapOneToOne`), then `ProbeCommand`,
  `HarnessCommand`, `GoalStart`. A new purpose is an arbiter ruling first.
- The command syntax (`-l`, `--`, single quotes, `'\''`) was proved against
  tmux 3.2a and 3.7c (README.md). A change to it repeats that proof and keeps
  `FuzzNewLiteral` green.
- Telemetry: `fleet_agent_typing_refused_total{reason}`, `typing.refused`
  and `typing.sent` carry ids and counts only (reason, purpose, server kind,
  pane id, bytes), never the text or a refused pane. A new
  `Reason` updates 05b in the same commit.
- The lint rules that guard this package are in `.golangci.yml`
  (`tmux-typing` and the `typing\.New` forbidigo pattern) and are proved by
  `scripts/test-lint-typing-rules.sh`. A new allowed caller is an
  `arbiter-fleet` ruling first, then the rule, the script and 01f F3.
- A change here updates this file, README.md and `doc.go` in the same commit.
