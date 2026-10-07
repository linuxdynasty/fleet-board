#!/usr/bin/env bash
#
# test-make-tools.sh: seeded RED/GREEN cases for two Makefile rules, run in a
# temp dir with a fake `go` first on PATH (so nothing is compiled or fetched):
#   - `make tools` rebuilds .bin/golangci-lint, .bin/gofumpt and
#     .bin/govulncheck when the Go version changes (including back to an older one), and each tool alone
#     when its own pin in tool-versions.env changes; otherwise it does nothing;
#   - `make gen` removes gen/ first, so a deleted proto leaves no stale code.
# The fake go answers `go env GOVERSION`, `go install ...@v` (creates the
# binary in $GOBIN and logs it) and `go run ... generate` (creates gen/fresh.txt).
# Runs on the macOS system bash 3.2. Needs make.
#
# Usage: scripts/test-make-tools.sh        exit 0 only if every case passes
set -u

root="$(cd "$(dirname "$0")/.." && pwd)"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
tree="$work/tree"
fakebin="$work/bin"
log="$work/go.log"
mkdir -p "$fakebin"

pass=0
fail=0

ok() { pass=$((pass + 1)); }
bad() {
  fail=$((fail + 1))
  printf 'FAIL: %s\n' "$1" >&2
}

cat > "$fakebin/go" <<EOF
#!/bin/sh
case "\$1" in
  env)
    echo "\${FAKE_GOVERSION:-go1.27.1}"
    ;;
  install)
    for last in "\$@"; do :; done
    name="\${last%@*}"
    name="\${name##*/}"
    mkdir -p "\$GOBIN"
    echo "built \$name with \${FAKE_GOVERSION:-go1.27.1}" > "\$GOBIN/\$name"
    chmod +x "\$GOBIN/\$name"
    echo "install \$name" >> "$log"
    ;;
  run)
    mkdir -p gen
    echo fresh > gen/fresh.txt
    echo "run generate" >> "$log"
    ;;
  *)
    echo "fake go: unexpected arguments: \$*" >&2
    exit 1
    ;;
esac
EOF
chmod +x "$fakebin/go"

rm -rf "$tree"
mkdir -p "$tree"
cp "$root/Makefile" "$root/tool-versions.env" "$tree/"

# mk <GOVERSION> <target>: run make in the temp tree and print the install log lines it added.
mk() {
  : > "$log"
  (cd "$tree" && PATH="$fakebin:$PATH" FAKE_GOVERSION="$1" make --no-print-directory "$2" > "$work/make.out" 2>&1)
  mk_rc=$?
  installs="$(tr '\n' ' ' < "$log" | sed 's/ $//')"
}

expect_installs() { # name want
  if [ "$mk_rc" -ne 0 ]; then
    bad "$1: make exited $mk_rc ($(cat "$work/make.out"))"
  elif [ "$installs" != "$2" ]; then
    bad "$1: installs were '$installs', want '$2'"
  else
    ok
  fi
}

all="install golangci-lint install gofumpt install govulncheck"

mk go1.27.1 tools
expect_installs "first make tools builds all three" "$all"

mk go1.27.1 tools
expect_installs "nothing changed: nothing is rebuilt" ""

mk go1.27.2 tools
expect_installs "a Go upgrade rebuilds all three" "$all"
if [ "$(cat "$tree/.bin/gofumpt")" != "built gofumpt with go1.27.2" ]; then
  bad "after the upgrade the binary is not the new build: $(cat "$tree/.bin/gofumpt")"
else
  ok
fi

mk go1.27.2 tools
expect_installs "same Go again: nothing is rebuilt" ""

mk go1.27.1 tools
expect_installs "going back to an older Go rebuilds all three" "$all"

touch "$tree/tool-versions.env"
mk go1.27.1 tools
expect_installs "touching tool-versions.env without a new pin rebuilds nothing" ""

sed 's/^GOFUMPT_VERSION=.*/GOFUMPT_VERSION=v0.99.0/' "$tree/tool-versions.env" > "$work/env.tmp" && mv "$work/env.tmp" "$tree/tool-versions.env"
mk go1.27.1 tools
expect_installs "a new gofumpt pin rebuilds only gofumpt" "install gofumpt"

sed 's/^GOLANGCI_LINT_VERSION=.*/GOLANGCI_LINT_VERSION=v2.99.0/' "$tree/tool-versions.env" > "$work/env.tmp" && mv "$work/env.tmp" "$tree/tool-versions.env"
mk go1.27.1 tools
expect_installs "a new golangci-lint pin rebuilds only golangci-lint" "install golangci-lint"

sed 's/^GOVULNCHECK_VERSION=.*/GOVULNCHECK_VERSION=v1.99.0/' "$tree/tool-versions.env" > "$work/env.tmp" && mv "$work/env.tmp" "$tree/tool-versions.env"
mk go1.27.1 tools
expect_installs "a new govulncheck pin rebuilds only govulncheck" "install govulncheck"

# gen removes gen/ first.
mkdir -p "$tree/gen/go/old"
echo stale > "$tree/gen/go/old/deleted_proto.pb.go"
mk go1.27.1 gen
if [ "$mk_rc" -ne 0 ]; then
  bad "make gen exited $mk_rc ($(cat "$work/make.out"))"
elif [ -e "$tree/gen/go/old/deleted_proto.pb.go" ]; then
  bad "make gen left stale generated code behind"
elif [ ! -f "$tree/gen/fresh.txt" ]; then
  bad "make gen did not generate"
else
  ok
fi

printf 'test-make-tools: %s passed, %s failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
