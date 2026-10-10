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
calls() {
  : > "$log"
  (cd "$root" && FLEET_LINT_CALLS="$log" BASE_REF=no-such-ref-for-the-selftest \
    make -f "$1" -o tools lint GOLANGCI_LINT="$stub" > "$work/make.out" 2>&1)
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

printf 'test-make-lint-platforms: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
