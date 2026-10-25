#!/usr/bin/env bash
#
# release-build.sh: build the release archives for one tag (plan row 16).
# Plain `go build` matrix, inside the 0.3 rules: no GoReleaser, no new tool.
#
# Usage: scripts/release-build.sh v0.1.0
#
# Builds fleetd, fleet-agent and fleet for darwin/linux amd64/arm64
# (CGO_ENABLED=0, the version stamped with -ldflags -X into
# internal/buildinfo), one archive per program and platform plus one
# SHA256SUMS file, all under dist/. Nothing else is published (02j).
# Run `make gen` first (gen/ is never committed), and for a real release
# build the dashboard first (`npm run build` in web/, so fleetd embeds it).
# Portable to the macOS system bash 3.2.
set -euo pipefail

tag="${1:-}"
case "$tag" in
  v[0-9]*.[0-9]*.[0-9]*) ;;
  *) echo "release-build: want a tag like v0.1.0, got '$tag'" >&2; exit 2 ;;
esac

root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$root"

[ -d gen ] || { echo "release-build: gen/ is missing; run make gen first" >&2; exit 1; }
command -v go >/dev/null || { echo "release-build: go is required" >&2; exit 1; }
command -v shasum >/dev/null || command -v sha256sum >/dev/null || {
  echo "release-build: shasum or sha256sum is required" >&2; exit 1; }

rm -rf dist stage
mkdir -p dist stage

for program in fleetd fleet-agent fleet; do
  for platform in "darwin amd64" "darwin arm64" "linux amd64" "linux arm64"; do
    set -- $platform
    goos="$1"
    goarch="$2"
    asset="$program-$goos-$goarch.tar.gz"
    echo "release-build: $asset"
    CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" go build \
      -ldflags "-X github.com/linuxdynasty/fleet-board/internal/buildinfo.Version=$tag" \
      -o "stage/$program" "./cmd/$program"
    rm -f "dist/$asset"
    tar -czf "dist/$asset" -C stage "$program"
    entries="$(tar -tzf "dist/$asset")"
    [ "$entries" = "$program" ] || {
      echo "release-build: $asset holds '$entries', want one file '$program'" >&2; exit 1; }
  done
done
rm -rf stage

cd dist
if command -v shasum >/dev/null; then
  shasum -a 256 ./*.tar.gz > SHA256SUMS
else
  sha256sum ./*.tar.gz > SHA256SUMS
fi
cd "$root"

echo "release-build: $tag"
ls dist/
