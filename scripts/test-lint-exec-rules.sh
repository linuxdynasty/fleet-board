#!/usr/bin/env bash
#
# test-lint-exec-rules.sh: seeded RED/GREEN cases for the two process rules in
# .golangci.yml (plan row 2c, 01 principle 8):
#
#   depguard   only internal/proc, internal/agent/tmux (the package itself, not
#              its subpackages) and internal/agent/attach may import os/exec or
#              golang.org/x/sys/execabs in internal/ and cmd/; test files may too.
#   forbidigo  os.StartProcess, syscall.ForkExec, syscall.Exec,
#              syscall.StartProcess and unix.Exec (golang.org/x/sys/unix) are
#              forbidden everywhere but internal/proc, through an import alias
#              and as a function value too.
#
# It builds a scratch module in a temporary folder (never this checkout), copies
# the real .golangci.yml into it and runs the pinned golangci-lint, once per
# target platform (linux and darwin, amd64 and arm64, cgo off, as `make lint`
# does; scripts/test-make-lint-platforms.sh checks that list), in three steps:
#   1. only the files the rules allow: must pass;
#   2. the same files plus one seeded violation per case: must fail, naming each
#      violating file with the right linter, and naming no allowed file. A file
#      built only for one OS or architecture (by file name or a `//go:build` line)
#      must be named in the runs for that platform and in no other: the linter
#      reads only the files that build for the platform it runs for, which is why
#      `make lint` runs them all;
#   3. the violations removed again: must pass.
# The allowed tmux, attach and proc folders do not exist in the repo yet; the
# rules name them so they are allowed when they arrive.
#
# Needs the pinned golangci-lint from `make tools` (.bin/golangci-lint, or
# $FLEET_GOLANGCI_LINT), go, and golang.org/x/sys in the module cache (go.mod
# requires it). Runs on the macOS system bash 3.2.
#
# Usage: scripts/test-lint-exec-rules.sh        exit 0 only if every case passes
set -u

root="$(cd "$(dirname "$0")/.." && pwd)"
lint="${FLEET_GOLANGCI_LINT:-$root/.bin/golangci-lint}"
if [ ! -x "$lint" ]; then
  printf 'test-lint-exec-rules: %s is missing; run `make tools` first\n' "$lint" >&2
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

# The scratch module resolves golang.org/x/sys from the module cache only.
sysver="$(cd "$root" && go list -m -f '{{.Version}}' golang.org/x/sys)" || exit 1
printf 'module example.test/probe\n\ngo 1.27\n\nrequire golang.org/x/sys %s\n' "$sysver" > "$probe/go.mod"
cp "$root/go.sum" "$probe/go.sum"
cp "$root/.golangci.yml" "$probe/.golangci.yml"
export GOFLAGS=-mod=mod GOPROXY=off

# put <path> <package> <import lines> <var lines>: one file in the scratch module.
put() {
  mkdir -p "$probe/$(dirname "$1")"
  printf 'package %s\n\nimport (\n%s\n)\n\nvar (\n%s\n)\n' "$2" "$3" "$4" > "$probe/$1"
}

# ---- allowed: must lint clean ----------------------------------------------
allowed="internal/proc/p.go internal/agent/tmux/t.go internal/agent/tmux/t_darwin.go internal/agent/tmux/t_arm64.go internal/agent/attach/a.go internal/ok/ok_test.go internal/ok/ok.go cmd/ok/ok_test.go cmd/ok/main.go"
put internal/proc/p.go proc '	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/unix"' '	_ = exec.Command
	_ = os.StartProcess
	_ = syscall.ForkExec
	_ = syscall.Exec
	_ = syscall.StartProcess
	_ = unix.Exec'
put internal/agent/tmux/t.go tmux '	"os/exec"' '	_ = exec.Command'
put internal/agent/tmux/t_darwin.go tmux '	"os/exec"' '	_ = exec.Command'
put internal/agent/tmux/t_arm64.go tmux '	"os/exec"
	"golang.org/x/sys/execabs"' '	_ = exec.Command
	_ = execabs.Command'
put internal/agent/attach/a.go attach '	"os/exec"' '	_ = exec.Command'
put internal/ok/ok.go ok '' ''
put internal/ok/ok_test.go ok '	"os/exec"' '	_ = exec.Command'
put cmd/ok/ok_test.go main '	"os/exec"' '	_ = exec.Command'
printf 'package main\n\nfunc main() {}\n' > "$probe/cmd/ok/main.go"

# ---- violations: "<path>|<linter>", one file each ---------------------------
# path|linter|platforms whose run must name it: all, an OS (linux, darwin) or an
# architecture (amd64, arm64)
violations="internal/red/import.go|depguard|all
internal/red/startprocess.go|forbidigo|all
internal/red/forkexec.go|forbidigo|all
internal/red/sysexec.go|forbidigo|all
internal/red/syscallstart.go|forbidigo|all
internal/red/unixexec.go|forbidigo|all
internal/red/alias.go|forbidigo|all
internal/red/funcvalue.go|forbidigo|all
internal/red/test_calls_test.go|forbidigo|all
internal/red/tag_darwin.go|depguard|darwin
internal/red/tag_linux.go|depguard|linux
internal/red/tagged_darwin_only.go|depguard|darwin
internal/red/tagged_linux_only.go|depguard|linux
internal/red/tagged_darwin_exec.go|forbidigo|darwin
internal/red/tagged_linux_exec.go|forbidigo|linux
internal/red/arch_arm64.go|depguard|arm64
internal/red/arch_amd64.go|depguard|amd64
internal/red/tagged_arm64_only.go|depguard|arm64
internal/red/tagged_amd64_only.go|depguard|amd64
internal/red/tagged_arm64_exec.go|forbidigo|arm64
internal/red/tagged_amd64_exec.go|forbidigo|amd64
internal/red/tagged_linux_arm64.go|depguard|linux/arm64
internal/red/execabs.go|depguard|all
internal/red/execabs_alias.go|depguard|all
internal/agent/tmux/sub/s.go|depguard|all
internal/agent/attach/sub/s.go|depguard|all
internal/agent/attachment/a.go|depguard|all
internal/agent/tmuxish/t.go|depguard|all
internal/proc/sub/s.go|depguard|all
internal/proc/sub/call.go|forbidigo|all
cmd/red/main.go|depguard|all"

# tagged <path> <package> <build line> <import lines> <var lines>
tagged() {
  mkdir -p "$probe/$(dirname "$1")"
  printf '//go:build %s\n\n' "$3" > "$probe/$1"
  printf 'package %s\n\nimport (\n%s\n)\n\nvar (\n%s\n)\n' "$2" "$4" "$5" >> "$probe/$1"
}

seed() {
  put internal/red/import.go red '	"os/exec"' '	_ = exec.Command'
  put internal/red/startprocess.go red '	"os"' '	_ = os.StartProcess'
  put internal/red/forkexec.go red '	"syscall"' '	_ = syscall.ForkExec'
  put internal/red/sysexec.go red '	"syscall"' '	_ = syscall.Exec'
  put internal/red/syscallstart.go red '	"syscall"' '	_ = syscall.StartProcess'
  put internal/red/unixexec.go red '	"golang.org/x/sys/unix"' '	_ = unix.Exec'
  put internal/red/alias.go red '	sx "golang.org/x/sys/unix"
	sc "syscall"' '	_ = sx.Exec
	_ = sc.Exec'
  put internal/red/funcvalue.go red '	"os"' '	start = os.StartProcess
	_     = start'
  put internal/red/test_calls_test.go red '	"syscall"' '	_ = syscall.Exec'
  # files built for one OS only: by file name, and by a build line
  put internal/red/tag_darwin.go red '	"os/exec"' '	_ = exec.Command'
  put internal/red/tag_linux.go red '	"os/exec"' '	_ = exec.Command'
  tagged internal/red/tagged_darwin_only.go red darwin '	"os/exec"' '	_ = exec.Command'
  tagged internal/red/tagged_linux_only.go red linux '	"os/exec"' '	_ = exec.Command'
  tagged internal/red/tagged_darwin_exec.go red darwin '	"syscall"' '	_ = syscall.Exec'
  tagged internal/red/tagged_linux_exec.go red linux '	"syscall"' '	_ = syscall.Exec'
  put internal/red/arch_arm64.go red '	"os/exec"' '	_ = exec.Command'
  put internal/red/arch_amd64.go red '	"os/exec"' '	_ = exec.Command'
  tagged internal/red/tagged_arm64_only.go red arm64 '	"os/exec"' '	_ = exec.Command'
  tagged internal/red/tagged_amd64_only.go red amd64 '	"os/exec"' '	_ = exec.Command'
  tagged internal/red/tagged_arm64_exec.go red arm64 '	"syscall"' '	_ = syscall.Exec'
  tagged internal/red/tagged_amd64_exec.go red amd64 '	"syscall"' '	_ = syscall.Exec'
  tagged internal/red/tagged_linux_arm64.go red 'linux && arm64' '	"os/exec"' '	_ = exec.Command'
  put internal/red/execabs.go red '	"golang.org/x/sys/execabs"' '	_ = execabs.Command'
  put internal/red/execabs_alias.go red '	ea "golang.org/x/sys/execabs"' '	_ = ea.Command'
  put internal/agent/tmux/sub/s.go sub '	"os/exec"' '	_ = exec.Command'
  put internal/agent/attach/sub/s.go sub '	"os/exec"' '	_ = exec.Command'
  put internal/agent/attachment/a.go attachment '	"os/exec"' '	_ = exec.Command'
  put internal/agent/tmuxish/t.go tmuxish '	"os/exec"' '	_ = exec.Command'
  put internal/proc/sub/s.go sub '	"os/exec"' '	_ = exec.Command'
  put internal/proc/sub/call.go sub '	"os"' '	_ = os.StartProcess' # a subfolder of internal/proc is not internal/proc
  mkdir -p "$probe/cmd/red"
  printf 'package main\n\nimport "os/exec"\n\nvar _ = exec.Command\n\nfunc main() {}\n' > "$probe/cmd/red/main.go"
}

unseed() {
  rm -rf "$probe/internal/red" "$probe/internal/agent/tmux/sub" "$probe/internal/agent/attach/sub" \
    "$probe/internal/agent/attachment" "$probe/internal/agent/tmuxish" "$probe/internal/proc/sub" "$probe/cmd/red"
}

out="$work/lint.out"
# runlint: the pinned linter for the platform of the loop, cgo off, every issue shown.
runlint() {
  (cd "$probe" && GOOS="$goos" GOARCH="$goarch" CGO_ENABLED=0 "$lint" run --config .golangci.yml --max-same-issues 0 --max-issues-per-linter 0 ./... > "$out" 2>&1)
}

# named <path> <linter>: the output has a finding of that linter on that file.
named() {
  awk -v p="$1" -v l="($2)" 'index($0, p ":") == 1 && substr($0, length($0) - length(l) + 1) == l { found = 1 } END { exit !found }' "$out"
}
# mentioned <path>: the output names the file at all.
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
      rest="${entry#*|}"
      linter="${rest%%|*}"
      oses="${rest##*|}"
      if [ "$oses" = all ] || [ "$oses" = "$goos" ] || [ "$oses" = "$goarch" ] || [ "$oses" = "$platform" ]; then
        if named "$path" "$linter"; then
          ok
        else
          bad "$platform: $path: no $linter finding in the output"
          rc=1
        fi
      elif mentioned "$path"; then
        bad "$platform: $path is built for $oses only, but this run names it"
        rc=1
      else
        ok
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

printf 'test-lint-exec-rules: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
