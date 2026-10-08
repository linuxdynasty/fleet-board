#!/usr/bin/env bash
#
# check-versions.sh: fail if a pinned tool version drifts from tool-versions.env.
#
# tool-versions.env is the one file that holds every pinned version. The
# Makefile includes it and CI sources it. Some things still carry a copy that
# a tool forces on us, so this script keeps each copy equal to the file:
#   - .node-version     (actions/setup-node reads it): major equals NODE_MAJOR
#   - go.mod            (setup-go reads it): the go line equals GO_VERSION
#   - buf.gen.yaml      protoc-gen-es@X equals PROTOC_GEN_ES_VERSION, and
#     protoc-gen-go / protoc-gen-connect-go equal the go.mod versions of
#     google.golang.org/protobuf / connectrpc.com/connect
#   - tools/tscheck/package.json: typescript equals TYPESCRIPT_VERSION and
#     @bufbuild/protobuf equals PROTOC_GEN_ES_VERSION (ADR 0020: same version)
# It also fails when the Makefile or a workflow spells out a pin of
# golangci-lint, buf, gofumpt, govulncheck, actionlint or benchstat instead of
# reading the file.
#
# Portable to the macOS system bash 3.2. FLEET_VERSIONS_REPO_ROOT points the
# script at another tree (used to test it against a seeded violation).
set -euo pipefail

root="${FLEET_VERSIONS_REPO_ROOT:-}"
if [ -z "$root" ]; then
  root="$(cd "$(dirname "$0")/.." && pwd)"
fi

problems=""
note() { problems="$problems
  $*"; }

versions="$root/tool-versions.env"
if [ ! -f "$versions" ]; then
  printf 'check-versions: missing %s\n' "$versions" >&2
  exit 1
fi
# shellcheck disable=SC1090
. "$versions"

for key in GO_VERSION NODE_MAJOR GOLANGCI_LINT_VERSION BUF_VERSION GOFUMPT_VERSION GOVULNCHECK_VERSION ACTIONLINT_VERSION BENCHSTAT_VERSION TYPESCRIPT_VERSION PROTOC_GEN_ES_VERSION; do
  eval "val=\${$key:-}"
  [ -n "$val" ] || note "tool-versions.env does not set $key"
done

if [ -n "${NODE_MAJOR:-}" ]; then
  if [ ! -f "$root/.node-version" ]; then
    note ".node-version is missing (setup-node reads it)"
  else
    nv="$(tr -d ' \r\n' < "$root/.node-version")"
    nv="${nv#v}"
    if [ "${nv%%.*}" != "$NODE_MAJOR" ]; then
      note ".node-version says $nv, tool-versions.env NODE_MAJOR is $NODE_MAJOR"
    fi
  fi
fi

if [ -f "$root/go.mod" ] && [ -n "${GO_VERSION:-}" ]; then
  gv="$(awk '$1 == "go" {print $2; exit}' "$root/go.mod")"
  if [ "$gv" != "$GO_VERSION" ]; then
    note "go.mod says go $gv, tool-versions.env GO_VERSION is $GO_VERSION"
  fi
fi

if [ -f "$root/buf.gen.yaml" ] && [ -n "${PROTOC_GEN_ES_VERSION:-}" ]; then
  if ! grep -q -F "protoc-gen-es@$PROTOC_GEN_ES_VERSION" "$root/buf.gen.yaml"; then
    note "buf.gen.yaml does not use protoc-gen-es@$PROTOC_GEN_ES_VERSION"
  fi
fi

# esc escapes the dots of a version for use inside a grep -E pattern. Versions
# hold only digits, dots, letters and hyphens, so the dot is the only special.
esc() { printf '%s' "$1" | sed 's/\./\\./g'; }

# The Go plugins run at the version of the library their output imports
# (go.mod), or the generated code and the runtime drift apart.
if [ -f "$root/buf.gen.yaml" ] && [ -f "$root/go.mod" ]; then
  for pair in "protoc-gen-go google.golang.org/protobuf" "protoc-gen-connect-go connectrpc.com/connect"; do
    plugin="${pair%% *}"
    module="${pair#* }"
    plugin_version="$(sed -n "s/.*${plugin}@\\(v[0-9A-Za-z.+-]*\\).*/\\1/p" "$root/buf.gen.yaml" | head -n 1)"
    [ -n "$plugin_version" ] || continue
    mod_version="$(awk -v mod="$module" '{ for (i = 1; i < NF; i++) if ($i == mod) { print $(i + 1); exit } }' "$root/go.mod")"
    if [ -z "$mod_version" ]; then
      note "buf.gen.yaml pins $plugin $plugin_version but go.mod does not require $module"
    elif [ "$plugin_version" != "$mod_version" ]; then
      note "buf.gen.yaml pins $plugin $plugin_version, go.mod requires $module $mod_version"
    fi
  done
fi

pkg="$root/tools/tscheck/package.json"
if [ -f "$pkg" ]; then
  if ! grep -q -E "\"typescript\": *\"$(esc "${TYPESCRIPT_VERSION:-unset}")\"" "$pkg"; then
    note "tools/tscheck/package.json does not pin typescript $TYPESCRIPT_VERSION"
  fi
  if ! grep -q -E "\"@bufbuild/protobuf\": *\"$(esc "${PROTOC_GEN_ES_VERSION:-unset}")\"" "$pkg"; then
    note "tools/tscheck/package.json does not pin @bufbuild/protobuf $PROTOC_GEN_ES_VERSION (same as protoc-gen-es)"
  fi
fi

# A literal pin in the Makefile or a workflow would be a second source of truth.
for f in "$root/Makefile" "$root"/.github/workflows/*.yml; do
  [ -f "$f" ] || continue
  hit="$(grep -n -E '(golangci-lint|cmd/buf|mvdan.cc/gofumpt|cmd/govulncheck|cmd/actionlint|cmd/benchstat)(/[a-z0-9/]*)?@v[0-9]' "$f" || true)"
  if [ -n "$hit" ]; then
    rel="${f#"$root"/}"
    note "$rel spells out a tool version (read tool-versions.env instead): $hit"
  fi
done

if [ -n "$problems" ]; then
  printf 'check-versions: pinned versions disagree:%s\n' "$problems" >&2
  exit 1
fi
printf 'check-versions: every pin matches tool-versions.env.\n'
