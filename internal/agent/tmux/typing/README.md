# internal/agent/tmux/typing

The one package that builds a tmux `send-keys` line (plan row 7, #140,
[01f](../../../../docs/specs/01f-agent-control-goals-and-policy.md) F3). Code
that wants to type into a pane asks it for a `Line`; the tmux client sends
that `Line` and nothing else.

## Use

```go
rec, err := typing.NewRecorder(tel)              // once, at wiring
l, err := typing.NewLiteral(rec, paneKey, typing.PurposeGoalStart, goalText)
k, err := typing.NewKeys(rec, paneKey, typing.PurposeGoalStart, typing.KeyEnter)
rec.Sent(ctx, l)                                  // after the client reports tmux took it
```

| Constructor | Builds |
|---|---|
| `NewLiteral(rec, pane, purpose, text)` | `send-keys -t <pane_id> -l -- '<text>'` |
| `NewKeys(rec, pane, purpose, keys...)` | `send-keys -t <pane_id> Enter Up ...` |

`-l` applies to every argument of one command, so text and named keys are
never in the same line: text then Enter is two lines.

## Rules

- `pane` is the whole `fleet.type.v1.PaneKey` (row-7 ruling, fork 11):
  `server` is `default`, `fleet` or `fleet-probe`; `server_start_id` is 1 to
  64 of `[a-z0-9-]`; `pane_id` is `%` and 1 to 10 digits. A bare pane id is
  refused: the `Line` keeps the server and start id, and the client refuses
  a line built for another server or an earlier start of it, so a line never
  lands on a pane id a restarted server reused. A session or window name
  could reach a pane nobody chose, and a space or `;` could end the target.
- `purpose` names the gate that must pass before the line is sent; the zero
  value is refused. `PromptAnswer`, `GoalNudge`, `GoalResume` and
  `OperatorText` equal the `SendKeysPurpose` values by name and number (a
  test holds it); `ProbeCommand` (row 8), `HarnessCommand` (row 18) and
  `GoalStart` (row 21) are gates that are not `SendKeys` gates (#140). Each
  later row implements its gate.
- Text is 1 to 4096 bytes (02e: `KeyInput.literal`) of UTF-8 with no control
  character. `\n` and `\r` are refused (`newline`): either one reaching a
  terminal is Enter. ESC, NUL, tab, DEL and C1 controls are refused
  (`control_char`); a key that needs one goes through `NewKeys`.
- Keys come from the closed set `Enter Escape Tab BSpace Up Down Left Right
  C-c`, 1 to 32 per line, numbered as `fleet.type.v1.NamedKey` (a test holds
  it). `NewKeys` accepts any key under any purpose: the gate each purpose
  names, in its caller, must check which keys it may press (C-c above all).
- A refusal returns the zero `Line` and a `*RefusedError` with its `Reason`.
  `Line.Command` of the zero `Line` returns `ErrZeroLine`.

## Why the quoting is safe

Inside single quotes tmux's command parser keeps every byte: no `;`
(second command), `$` (variable), `~` (home folder), `#{}` (format), `{}`
(block), `#` (comment) or `\` escape. A `'` in the text becomes `'\''`. `--`
ends the flags, so text starting with `-` is not read as one. With newlines
refused, a line is always one command.

Proved on 2026-10-10 on macOS against tmux 3.2a (built from the release
tarball, SHA-256 `551553a4…cd05f`, as in
[spikes/tmuxtyping](../../../../spikes/tmuxtyping/results/README.md)) and
Homebrew tmux 3.7c, each on a throwaway server (`-S` in a short temporary
folder, `-f /dev/null`, `TMUX` unset, `kill-server` after, `has-session`
failing): a control client sent
`send-keys -t %0 -l -- '~/a it'\''s ; x \; $HOME #{pane_id} {b} # c "q" \\ \n é 🙂 -- -z %if'`
to a pane running `cat`, and the pane showed the text byte for byte on both.
Without `--`, `send-keys -t %0 -l '-x'` is a parse error on both
("unknown flag -x" on 3.7c, the usage line on 3.2a). `FuzzNewLiteral` holds
every built line to the same syntax. The client test against real tmux in CI
arrives with plan row 7 PR 7c2.

## Lint rules

`.golangci.yml` has three, proved by `scripts/test-lint-typing-rules.sh`:

- `depguard` (`tmux-typing`): only `internal/agent/control`, `start`, `probe`
  and `tmux` (each package itself, not a subpackage) import this package;
  test files may.
- `forbidigo` (`^typing\.New`, type-aware): only `control`, `start` and
  `probe` call or reference a `New…` function, through an import alias or as
  a function value too; test files and this package are excepted.
- `depguard` (`no-unsafe`): no `unsafe` import in `internal/` or `cmd/`
  outside tests. `//go:linkname` needs it, and a linkname to `NewLiteral`
  would build a `Line` with no call the forbidigo pattern can see.

## Telemetry

| Signal | What it says |
|---|---|
| `fleet_agent_typing_refused_total{reason}` | lines refused, by reason |
| `typing.refused` (warn; `reason`, `purpose`) | one line per refusal |
| `typing.sent` (info; `purpose`, `server_kind`, `pane_id`, `bytes`) | one line per line tmux took; the caller that built the line calls `Recorder.Sent` (the client holds no Recorder: `NewRecorder` is a `New…` function, so only control, start and probe can make one) |

Ids and counts only: never the text, never a refused pane. 3 AM question: "why did the agent
type nothing into that pane?" ([05b](../../../../docs/specs/05b-observability.md)).
