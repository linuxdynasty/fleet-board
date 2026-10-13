# internal/harness

What every harness adapter shares: the strict version check (01 C1) and the
shared conformance test (plan row 8). The per-harness adapters live in the
subfolders (`claude/`, `codex/`, `cursor/`, `muse/`, `opencode/`), as parsers
plus recorded screens under each one's `testdata/<version>/` (first parser:
`cursor/` reads the `/usage` dialog, row 30).

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

Tests: `CGO_ENABLED=0 go test -shuffle=on -count=3 ./internal/harness/`.
`TestDetectThroughProc` runs the real helper around `go version`, so it needs
a Go toolchain and nothing else; the adapter fixtures need no harness.
