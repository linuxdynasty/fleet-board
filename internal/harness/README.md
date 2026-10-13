# internal/harness

What every harness adapter shares: the strict version check (01 C1), the
shared conformance test (plan row 8) and the reset-time helpers the usage
parsers share. The per-harness adapters live in the subfolders (`claude/`,
`codex/`, `cursor/`, `muse/`, `opencode/`), as parsers plus recorded screens
under each one's `testdata/<version>/`. Usage parsers so far: `cursor/`
reads the `/usage` dialog (row 30), `claude/`, `codex/` and `muse/`
read their `/usage` and `/status` screens (row 8), and `opencode/` reads
the `stats` spend table, the `providers list` account output and the TUI
model line (row 8, issue #36).

- `version.go`: `Detector` runs a harness's version command through
  `internal/proc` under the `version` caller (empty stdin, 5 s, 16 KiB).
  `ParseVersionOutput` reads the first line: a dotted number such as `1.4.2`
  is the version, anything else is `not_on_path`, `empty_version_output` or
  `invalid_version_output` (a wrapper that exits 0 and prints an install
  prompt is not a version). A missing version counts as a failed `Detect()`.
- `conformance.go`: `Conform` is what every adapter's test calls over every
  fixture of every version it supports. `Check` holds the rules: the declared
  support must match the committed golden table (`SupportTable`), a version
  marked `SUPPORTED` must have every feature supported, a supported feature
  must bring a fixture of that version, and every fixture must parse or
  cleanly yield unknown, never fail.
- `resettime.go`: the calendar half every usage parser shares. Reset texts
  have no year and are local (01 D2), so `NextMonthDayTime` resolves one to
  the next such date and time after the capture and `NextTimeOfDay` to today
  or tomorrow; an impossible day (Feb 30) stays unknown. `StripControls`,
  `MaxScreenBytes`, `Clock12`, `MonthFromShort` and the `Percent` builders
  keep each adapter's own file down to its labels and shapes.

Tests: `CGO_ENABLED=0 go test -shuffle=on -count=3 ./internal/harness/`.
`TestDetectThroughProc` runs the real helper around `go version`, so it needs
a Go toolchain and nothing else; the adapter fixtures need no harness.
