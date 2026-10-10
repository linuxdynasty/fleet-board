#!/usr/bin/env bash
#
# test-lint-typing-rules.sh: seeded RED/GREEN cases for the two typing rules
# in .golangci.yml (plan row 7, #140; 01f F3):
#
#   depguard   only internal/agent/control, internal/agent/start,
#              internal/agent/probe and internal/agent/tmux (each package
#              itself, not its subpackages; tmux only to name typing.Line)
#              may import internal/agent/tmux/typing; test files may too.
#   forbidigo  only control, start and probe may call or reference a
#              typing.New… function, through an import alias and as a
#              function value too; test files and typing itself excepted.
#              The other forbidigo patterns (the process rules) still apply
#              in those packages.
#
# It builds a scratch module named like the real one (the rules match the
# typing import path) in a temporary folder, with a stub typing package (a
# Line and its constructors), copies the real .golangci.yml into it and runs
# the pinned golangci-lint, once per target platform (as `make lint` does),
# in three steps:
#   1. only the files the rules allow: must pass;
#   2. the same files plus one seeded violation per case: must fail, naming
#      each violating file with the right linter, and naming no allowed file;
#   3. the violations removed again: must pass.
# The fifth planted case of #35, a zero typing.Line, is not a lint finding:
# the scratch tmux package holds one (zero.go), which must lint clean, and the
# typing package's TestZeroLineIsRefused shows Command refuses it; the client
# that refuses it at run time arrives with plan row 7 PR 7c2.
#
# Needs the pinned golangci-lint from `make tools` (.bin/golangci-lint, or
# $FLEET_GOLANGCI_LINT) and go. Runs on the macOS system bash 3.2.
#
# Usage: scripts/test-lint-typing-rules.sh        exit 0 only if every case passes
set -u

root="$(cd "$(dirname "$0")/.." && pwd)"
lint="${FLEET_GOLANGCI_LINT:-$root/.bin/golangci-lint}"
if [ ! -x "$lint" ]; then
  printf 'test-lint-typing-rules: %s is missing; run `make tools` first\n' "$lint" >&2
  exit 1
fi
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
probe="$work/probe"
mkdir -p "$probe"

pass=0
fail=0
ok() { pass=$((pass + 1)); }
bad() {
  fail=$((fail + 1))
  printf 'FAIL: %s\n' "$1" >&2
}

mod=github.com/linuxdynasty/fleet-board
ty="$mod/internal/agent/tmux/typing"
printf 'module %s\n\ngo 1.27\n' "$mod" > "$probe/go.mod"
cp "$root/.golangci.yml" "$probe/.golangci.yml"
export GOFLAGS=-mod=mod GOPROXY=off

# put <path> <package> <import lines> <body>: one file in the scratch module.
put() {
  mkdir -p "$probe/$(dirname "$1")"
  printf 'package %s\n\nimport (\n%s\n)\n\n%s\n' "$2" "$3" "$4" > "$probe/$1"
}

# The stub typing package: the real one's shape, calling its own constructor.
mkdir -p "$probe/internal/agent/tmux/typing"
cat > "$probe/internal/agent/tmux/typing/typing.go" << 'EOF'
package typing

type Purpose int

type Line struct{ cmd string }

func (l Line) IsZero() bool { return l.cmd == "" }

func NewLiteral(pane string, p Purpose, text string) (Line, error) { return Line{cmd: text}, nil }

func NewKeys(pane string, p Purpose) (Line, error) { return NewLiteral(pane, p, "Enter") }

var _ = NewKeys
EOF

# a call inside a function literal: a reference the linters see, with no
# result thrown away (staticcheck SA4017)
call='var _ = func() (typing.Line, error) { return typing.NewLiteral("%1", 1, "x") }'
usesline='func Send(l typing.Line) bool { return !l.IsZero() }'

# ---- allowed: must lint clean ----------------------------------------------
allowed="internal/agent/tmux/typing/typing.go internal/agent/control/c.go internal/agent/start/s.go internal/agent/probe/p.go internal/agent/control/alias.go internal/agent/control/start.go internal/agent/tmux/t.go internal/agent/tmux/zero.go internal/agent/tmux/t_test.go internal/agent/goals/g_test.go internal/agent/goals/g.go cmd/ok/ok_test.go cmd/ok/main.go"
put internal/agent/control/c.go control "	\"$ty\"" "$call"
put internal/agent/start/s.go start "	\"$ty\"" "$call
var _ = typing.NewKeys"
put internal/agent/probe/p.go probe "	\"$ty\"" "$call"
put internal/agent/control/alias.go control "	tp \"$ty\"" 'var _ = tp.NewKeys'
# the typing waiver is for the typing rule only: os.StartProcess in an allowed
# package must still be named by the process rule (see the violations)
put internal/agent/tmux/t.go tmux "	\"$ty\"" "$usesline"
put internal/agent/tmux/zero.go tmux "	\"$ty\"" 'var zero typing.Line

var _ = Send(zero)'
put internal/agent/tmux/t_test.go tmux "	\"$ty\"" "$call"
put internal/agent/goals/g_test.go goals "	\"$ty\"" "$call"
put cmd/ok/ok_test.go main "	\"$ty\"" "$call"
printf 'package main\n\nfunc main() {}\n' > "$probe/cmd/ok/main.go"
mkdir -p "$probe/internal/agent/goals"
printf 'package goals\n' > "$probe/internal/agent/goals/g.go"
printf 'package control\n\nimport "os"\n\nvar _ = os.Getpid\n' > "$probe/internal/agent/control/start.go"

# ---- violations: "<path>|<linter>", one file each ---------------------------
violations="internal/agent/goals/import.go|depguard
internal/agent/tmux/call.go|forbidigo
internal/agent/tmux/alias.go|forbidigo
internal/agent/tmux/funcvalue.go|forbidigo
internal/agent/tmux/dot.go|forbidigo
internal/agent/tmux/sub/s.go|depguard
internal/agent/tmux/sub/s.go|forbidigo
internal/agent/controlx/c.go|depguard
internal/agent/controlx/c.go|forbidigo
internal/agent/control/sub/c.go|depguard
internal/agent/control/sub/c.go|forbidigo
internal/agent/probe/proc.go|forbidigo
internal/red/import.go|depguard
cmd/red/main.go|depguard"

seed() {
  put internal/agent/goals/import.go goals "	\"$ty\"" 'var _ typing.Line'
  put internal/agent/tmux/call.go tmux "	\"$ty\"" "$call"
  put internal/agent/tmux/alias.go tmux "	tp \"$ty\"" 'var _ = func() (tp.Line, error) { return tp.NewKeys("%1", 1) }'
  put internal/agent/tmux/funcvalue.go tmux "	\"$ty\"" 'var build = typing.NewLiteral

var _ = build'
  put internal/agent/tmux/dot.go tmux "	. \"$ty\"" 'var _ = NewKeys'
  put internal/agent/tmux/sub/s.go sub "	\"$ty\"" "$call"
  put internal/agent/controlx/c.go controlx "	\"$ty\"" "$call"
  put internal/agent/control/sub/c.go sub "	\"$ty\"" "$call"
  # the process rule still holds in a package the typing rule lets through
  put internal/agent/probe/proc.go probe '	"os"' 'var _ = os.StartProcess'
  put internal/red/import.go red "	\"$ty\"" 'var _ typing.Line'
  mkdir -p "$probe/cmd/red"
  printf 'package main\n\nimport "%s"\n\nvar _ typing.Line\n\nfunc main() {}\n' "$ty" > "$probe/cmd/red/main.go"
}

unseed() {
  rm -rf "$probe/internal/agent/goals/import.go" "$probe/internal/agent/tmux/call.go" \
    "$probe/internal/agent/tmux/alias.go" "$probe/internal/agent/tmux/funcvalue.go" \
    "$probe/internal/agent/tmux/dot.go" "$probe/internal/agent/tmux/sub" "$probe/internal/agent/controlx" \
    "$probe/internal/agent/control/sub" "$probe/internal/agent/probe/proc.go" "$probe/internal/red" "$probe/cmd/red"
}

out="$work/lint.out"
runlint() {
  (cd "$probe" && GOOS="$goos" GOARCH="$goarch" CGO_ENABLED=0 "$lint" run --config .golangci.yml --max-same-issues 0 --max-issues-per-linter 0 ./... > "$out" 2>&1)
}

# named <path> <linter>: the output has a finding of that linter on that file.
named() {
  awk -v p="$1" -v l="($2)" 'index($0, p ":") == 1 && substr($0, length($0) - length(l) + 1) == l { found = 1 } END { exit !found }' "$out"
}
mentioned() {
  awk -v p="$1" 'index($0, p ":") == 1 { found = 1 } END { exit !found }' "$out"
}

for platform in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64; do
  goos="${platform%/*}"
  goarch="${platform#*/}"
  # 1. allowed files only
  if runlint; then
    ok
  else
    bad "$platform: the allowed files fail the lint: $(cat "$out")"
  fi

  # 2. seeded violations
  seed
  if runlint; then
    bad "$platform: the seeded violations passed the lint"
  else
    rc=0
    for entry in $violations; do
      path="${entry%%|*}"
      linter="${entry#*|}"
      if named "$path" "$linter"; then
        ok
      else
        bad "$platform: $path: no $linter finding in the output"
        rc=1
      fi
    done
    for path in $allowed; do
      if mentioned "$path"; then
        bad "$platform: the allowed file $path is named in the output"
        rc=1
      fi
    done
    if [ "$rc" -ne 0 ]; then
      sed 's/^/  | /' "$out" >&2
    fi
  fi

  # 3. violations removed
  unseed
  if runlint; then
    ok
  else
    bad "$platform: the lint fails after the violations were removed: $(cat "$out")"
  fi
done

printf 'test-lint-typing-rules: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
