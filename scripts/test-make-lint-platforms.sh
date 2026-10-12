#!/usr/bin/env bash
#
# test-make-lint-platforms.sh: `make lint` must run the linter for every
# platform the binaries ship for (linux and darwin, amd64 and arm64, cgo off),
# because golangci-lint reads only the files that build for the GOOS and GOARCH
# it runs for (01 principle 8; scripts/test-lint-exec-rules.sh seeds such files).
#
# It runs the real Makefile's lint target with a stub in place of the linter
# (GOLANGCI_LINT=<stub>, `-o tools` so nothing is built, an unknown BASE_REF so
# every package counts) and reads which GOOS, GOARCH and CGO_ENABLED the stub was
# called with. GREEN: the real Makefile calls it once for each of the four
# combinations, cgo off. RED: copies of the Makefile with a platform taken out,
# or the arch loop removed, must fail the same check.
#
# The package list must be worked out per platform as well: go list leaves out
# a package none of whose files build for the platform, so a NEW package with a
# single d_darwin.go (or l_linux.go, a_arm64.go) is linted on the platform it
# builds for and skipped on the others, not skipped everywhere (a Linux CI host)
# or handed to a run that fails with "build constraints exclude all Go files"
# (a Mac host). A second part runs the real Makefile and the real linter in a
# throwaway git repo: a violating single-platform package must fail `make lint`
# and a clean one must pass; a Makefile that computes the list once must fail
# one of those on this host.
#
# Runs on the macOS system bash 3.2. Needs make and the repo's own files.
#
# Usage: scripts/test-make-lint-platforms.sh        exit 0 only if every case passes
set -u

root="$(cd "$(dirname "$0")/.." && pwd)"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

pass=0
fail=0
ok() { pass=$((pass + 1)); }
bad() {
  fail=$((fail + 1))
  printf 'FAIL: %s\n' "$1" >&2
}

stub="$work/linter"
log="$work/calls"
cat > "$stub" <<'STUB'
#!/bin/sh
# One line per call: the platform and cgo setting it ran with, and its arguments.
printf '%s/%s cgo=%s args=%s\n' "${GOOS:-unset}" "${GOARCH:-unset}" "${CGO_ENABLED:-unset}" "$*" >> "$FLEET_LINT_CALLS"
STUB
chmod +x "$stub"

# calls <Makefile>: run its lint target against the stub and print what it called.
# BASE_REF rides the command line, not the environment: a forced BASE_REF from
# MAKEOVERRIDES (the pre-push hook names origin/main on its make command line)
# would otherwise beat the unknown ref and shrink the package list, so a branch
# with no Go change could never see all four platforms.
calls() {
  : > "$log"
  (cd "$root" && FLEET_LINT_CALLS="$log" \
    make -f "$1" -o tools lint GOLANGCI_LINT="$stub" BASE_REF=no-such-ref-for-the-selftest > "$work/make.out" 2>&1)
  return $?
}

# covers <Makefile>: 0 only when the stub saw exactly the four platforms, once
# each, with cgo off and every package.
covers() {
  calls "$1" || return 1
  got="$(sort "$log")"
  want="$(printf '%s\n' \
    'darwin/amd64 cgo=0 args=run ./...' \
    'darwin/arm64 cgo=0 args=run ./...' \
    'linux/amd64 cgo=0 args=run ./...' \
    'linux/arm64 cgo=0 args=run ./...')"
  [ "$got" = "$want" ]
}

# GREEN: the real Makefile.
if covers "$root/Makefile"; then
  ok
else
  bad "the real Makefile does not lint all four platforms: $(cat "$log" "$work/make.out" 2> /dev/null)"
fi

# RED: a platform taken out, a whole OS, and the arch left to the host.
mutant() {
  sed "$2" "$root/Makefile" > "$work/Makefile.$1"
  if cmp -s "$root/Makefile" "$work/Makefile.$1"; then
    bad "$1: the mutation changed nothing, so the case proves nothing"
  elif covers "$work/Makefile.$1"; then
    bad "$1: a Makefile that skips a platform passed the check"
  else
    ok
  fi
}
mutant no-linux-arm64 's|^LINT_PLATFORMS := linux/amd64 linux/arm64 |LINT_PLATFORMS := linux/amd64 |'
mutant no-darwin 's|^LINT_PLATFORMS := .*|LINT_PLATFORMS := linux/amd64 linux/arm64|'
mutant no-arm64 's|^LINT_PLATFORMS := .*|LINT_PLATFORMS := linux/amd64 darwin/amd64|'
mutant arch-left-to-host 's|GOARCH=$$goarch ||'
mutant cgo-on 's|CGO_ENABLED=0 \$(GOLANGCI_LINT)|CGO_ENABLED=1 $(GOLANGCI_LINT)|'

# ---- the package list is per platform ---------------------------------------
lint="${FLEET_GOLANGCI_LINT:-$root/.bin/golangci-lint}"
if [ ! -x "$lint" ]; then
  printf 'test-make-lint-platforms: %s is missing; run `make tools` first\n' "$lint" >&2
  exit 1
fi
repo="$work/repo"
mkdir -p "$repo/scripts" "$repo/internal/base"
cp "$root/tool-versions.env" "$root/.golangci.yml" "$repo/"
cp "$root/scripts/changed-go-packages.sh" "$repo/scripts/"
printf 'module example.test/probe\n\ngo 1.27\n' > "$repo/go.mod"
printf 'package base\n' > "$repo/internal/base/base.go"
unset GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null
git -C "$repo" init -q
git -C "$repo" add -A
git -C "$repo" -c user.name=t -c user.email=t@example.test commit -q -m base

# newpkg <dir> <file> <violation: yes|no>: an untracked package of one file.
newpkg() {
  mkdir -p "$repo/$1"
  if [ "$3" = yes ]; then
    printf 'package p\n\nimport "os/exec"\n\nvar _ = exec.Command\n' > "$repo/$1/$2"
  else
    printf 'package p\n' > "$repo/$1/$2"
  fi
}
clean() { rm -rf "$repo/internal/donly" "$repo/internal/lonly" "$repo/internal/aonly"; }

# makelint <Makefile>: make lint in the throwaway repo, the real linter, the
# base being HEAD so only the new files count.
makelint() {
  (cd "$repo" && make -f "$1" -o tools lint GOLANGCI_LINT="$lint" BASE_REF=HEAD > "$work/lint.out" 2>&1)
}

# perplatform <Makefile>: 0 only when a violating single-platform package fails
# make lint (each of darwin-only, linux-only and arm64-only) and clean ones pass.
perplatform() {
  mk="$1"
  clean
  newpkg internal/donly d_darwin.go no
  newpkg internal/lonly l_linux.go no
  newpkg internal/aonly a_arm64.go no
  if ! makelint "$mk"; then
    echo "clean single-platform packages failed make lint: $(tail -5 "$work/lint.out")"
    clean
    return 1
  fi
  for case in internal/donly/d_darwin.go internal/lonly/l_linux.go internal/aonly/a_arm64.go; do
    clean
    newpkg "$(dirname "$case")" "$(basename "$case")" yes
    if makelint "$mk"; then
      echo "a violation in the new package $case passed make lint"
      clean
      return 1
    fi
  done
  clean
  return 0
}

if why="$(perplatform "$root/Makefile")"; then
  ok
else
  bad "the real Makefile: $why"
fi

sed 's|pkgs="$$(GOOS=$$goos GOARCH=$$goarch CGO_ENABLED=0 scripts/changed-go-packages.sh|pkgs="$$(scripts/changed-go-packages.sh|' "$root/Makefile" > "$work/Makefile.once"
if cmp -s "$root/Makefile" "$work/Makefile.once"; then
  bad "list-once: the mutation changed nothing, so the case proves nothing"
elif why="$(perplatform "$work/Makefile.once")"; then
  bad "list-once: a Makefile that computes the package list once for the host passed"
else
  printf 'list-once is caught: %s\n' "$why"
  ok
fi

printf 'test-make-lint-platforms: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
