#!/usr/bin/env bash
#
# test-check-versions.sh: seeded RED/GREEN cases for scripts/check-versions.sh.
#
# Builds a small consistent tree in a temp dir (GREEN), then breaks one pin at
# a time (RED) and checks the checker fails and names the problem. Runs on the
# macOS system bash 3.2.
#
# Usage: scripts/test-check-versions.sh        exit 0 only if every case passes
set -u

root="$(cd "$(dirname "$0")/.." && pwd)"
check="$root/scripts/check-versions.sh"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

pass=0
fail=0

ok() { pass=$((pass + 1)); }
bad() {
  fail=$((fail + 1))
  printf 'FAIL: %s\n' "$1" >&2
}

# fresh: write a consistent tree at $work/t.
fresh() {
  local d="$work/t"
  rm -rf "$d"
  mkdir -p "$d/.github/workflows" "$d/tools/tscheck"
  cat > "$d/tool-versions.env" <<'ENV'
# test copy
GO_VERSION=1.27
NODE_MAJOR=24
GOLANGCI_LINT_VERSION=v2.14.0
BUF_VERSION=v1.73.0
GOFUMPT_VERSION=v0.12.0
GOVULNCHECK_VERSION=v1.8.0
ACTIONLINT_VERSION=v1.7.12
TYPESCRIPT_VERSION=7.0.2
PROTOC_GEN_ES_VERSION=2.16.0
ENV
  printf '24\n' > "$d/.node-version"
  cat > "$d/go.mod" <<'GOMOD'
module example.com/x

go 1.27

require (
	connectrpc.com/connect v1.21.0
	google.golang.org/protobuf v1.36.12
)
GOMOD
  cat > "$d/buf.gen.yaml" <<'YAML'
plugins:
  - local: [go, run, google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.12]
  - local: [go, run, connectrpc.com/connect/cmd/protoc-gen-connect-go@v1.21.0]
  - local: [npx, --yes, "@bufbuild/protoc-gen-es@2.16.0"]
YAML
  cat > "$d/tools/tscheck/package.json" <<'JSON'
{
  "devDependencies": {
    "typescript": "7.0.2"
  },
  "dependencies": {
    "@bufbuild/protobuf": "2.16.0"
  }
}
JSON
  printf 'include tool-versions.env\n' > "$d/Makefile"
  printf 'steps:\n  - run: . ./tool-versions.env\n' > "$d/.github/workflows/ci.yml"
}

# edit <file> <sed expression>: rewrite a file of the temp tree.
edit() {
  sed "$2" "$work/t/$1" > "$work/edit.tmp" && mv "$work/edit.tmp" "$work/t/$1"
}

# expect <name> <want rc> <substring of the output, or empty>
expect() {
  local name="$1" want="$2" sub="$3" out rc
  out="$(FLEET_VERSIONS_REPO_ROOT="$work/t" "$check" 2>&1)"
  rc=$?
  if [ "$rc" -ne "$want" ]; then
    bad "$name: exit $rc, want $want ($out)"
    return
  fi
  if [ -n "$sub" ]; then
    case "$out" in
      *"$sub"*) ;;
      *)
        bad "$name: output lacks '$sub' ($out)"
        return
        ;;
    esac
  fi
  ok
}

fresh
expect "consistent tree passes" 0 ""

fresh
printf '22\n' > "$work/t/.node-version"
expect "node-version major differs" 1 ".node-version"

fresh
edit go.mod 's/^go 1.27$/go 1.26/'
expect "go.mod go line differs" 1 "go.mod"

fresh
rm "$work/t/go.mod"
expect "missing go.mod is allowed" 0 ""

fresh
edit buf.gen.yaml 's/protoc-gen-es@2.16.0/protoc-gen-es@2.15.0/'
expect "protoc-gen-es differs from buf.gen.yaml" 1 "buf.gen.yaml"

fresh
edit buf.gen.yaml 's/protoc-gen-go@v1.36.12/protoc-gen-go@v1.36.11/'
expect "protoc-gen-go differs from go.mod's protobuf" 1 "protoc-gen-go"

fresh
edit buf.gen.yaml 's/protoc-gen-connect-go@v1.21.0/protoc-gen-connect-go@v1.20.0/'
expect "protoc-gen-connect-go differs from go.mod's connect" 1 "protoc-gen-connect-go"

fresh
edit go.mod 's#google.golang.org/protobuf v1.36.12#google.golang.org/protobuf v1.36.13#'
expect "go.mod protobuf moved without buf.gen.yaml" 1 "protoc-gen-go"

fresh
edit go.mod '/google.golang.org\/protobuf/d'
expect "go.mod without protobuf while buf.gen.yaml pins protoc-gen-go" 1 "google.golang.org/protobuf"

fresh
edit tools/tscheck/package.json 's/"7.0.2"/"7x0x2"/'
expect "a version that only matches if dots are wildcards fails" 1 "typescript"

fresh
edit tools/tscheck/package.json 's/7.0.2/6.0.3/'
expect "typescript differs from package.json" 1 "package.json"

fresh
edit tools/tscheck/package.json 's/2.16.0/2.15.0/'
expect "@bufbuild/protobuf differs from protoc-gen-es" 1 "@bufbuild/protobuf"

fresh
edit tool-versions.env '/^BUF_VERSION/d'
expect "a missing key is named" 1 "BUF_VERSION"

fresh
edit tool-versions.env '/^ACTIONLINT_VERSION/d'
expect "a missing ACTIONLINT_VERSION is named" 1 "ACTIONLINT_VERSION"

fresh
edit tool-versions.env '/^GOVULNCHECK_VERSION/d'
expect "a missing GOVULNCHECK_VERSION is named" 1 "GOVULNCHECK_VERSION"

fresh
printf 'vuln:\n\tgo install golang.org/x/vuln/cmd/govulncheck@v1.8.0\n' >> "$work/t/Makefile"
expect "govulncheck pin repeated in the Makefile" 1 "Makefile"

fresh
printf '  - run: go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12\n' >> "$work/t/.github/workflows/ci.yml"
expect "actionlint pin repeated in a workflow" 1 "ci.yml"

fresh
printf 'lint:\n\tgo install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0\n' >> "$work/t/Makefile"
expect "pin repeated in the Makefile" 1 "Makefile"

fresh
printf '  - run: go run github.com/bufbuild/buf/cmd/buf@v1.73.0 build\n' >> "$work/t/.github/workflows/ci.yml"
expect "pin repeated in a workflow" 1 "ci.yml"

printf 'test-check-versions: %s passed, %s failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
