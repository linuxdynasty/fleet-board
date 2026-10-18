#!/usr/bin/env bash
# Select the same tmux releases as CI for the local gate. Builds are private to
# this worktree. A complete build is published with one atomic symlink, so two
# gates may build together without ever selecting half an installation.
set -euo pipefail

# A gate build uses known compiler settings. Ignore ambient configure and make
# overrides, so they cannot change a binary behind an existing cache key.
unset CC CFLAGS CXXFLAGS CPPFLAGS LDFLAGS LIBS CONFIG_SITE PKG_CONFIG_PATH PKG_CONFIG_LIBDIR
unset TMUX_CONFIGURE_ARGS MAKEFLAGS MFLAGS GNUMAKEFLAGS

root="$(cd "$(dirname "$0")/.." && pwd -P)"
# shellcheck disable=SC1091
. "$root/tool-versions.env"
cache="$root/.bin/tmux-local"

die() { printf 'local-tmux: %s\n' "$*" >&2; exit 1; }
digest() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | cut -d ' ' -f 1
  else shasum -a 256 "$1" | cut -d ' ' -f 1; fi
}
digest_input() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum | cut -d ' ' -f 1
  else shasum -a 256 | cut -d ' ' -f 1; fi
}
installer_hash="$(digest "$root/scripts/install-tmux.sh")" || die 'cannot hash install-tmux.sh'
selector_hash="$(digest "$root/scripts/local-tmux.sh")" || die 'cannot hash local-tmux.sh'
# Compiler and library versions are build inputs too. These are local queries;
# a warm cache needs no network. The installer still verifies the tarball hash.
compiler="$(command -v cc || true)"
compiler_version="$(cc --version 2>/dev/null || true)"
libraries="$(pkg-config --modversion libevent ncurses 2>/dev/null || true)"
if [ "$(uname -s)" = Darwin ] && command -v brew >/dev/null 2>&1; then
  libraries="$libraries$(brew list --versions libevent ncurses 2>/dev/null || true)
$(brew --prefix libevent 2>/dev/null || true)
$(brew --prefix ncurses 2>/dev/null || true)"
fi
build_hash="$(printf '%s\n' "$installer_hash" "$selector_hash" "$compiler" "$compiler_version" "$libraries" | digest_input)"
platform="$(uname -s)-$(uname -m)"

pin() {
  case "$1" in
    32) version="$TMUX_32_VERSION" sum="$TMUX_32_SHA256" ;;
    latest) version="$TMUX_LATEST_VERSION" sum="$TMUX_LATEST_SHA256" ;;
    *) die "unknown pin $1" ;;
  esac
  case "$version" in ''|*[!a-zA-Z0-9.]*) die "invalid tmux version $version" ;; esac
  case "$sum" in *[!a-fA-F0-9]*|'') die "invalid SHA-256 for tmux $version" ;; esac
  [ "${#sum}" -eq 64 ] || die "invalid SHA-256 for tmux $version"
  key="$version-$sum-$platform-$build_hash"
  link="$cache/$key"
  binary="$link/install/bin/tmux"
}

verify() {
  [ -L "$link" ] || die "incomplete cache for tmux $version at $link"
  target="$(readlink "$link")"
  case "$target" in .build.*) ;; *) die "incomplete cache for tmux $version at $link" ;; esac
  [ -f "$cache/$target/.pin" ] && [ -x "$binary" ] || die "incomplete cache for tmux $version at $link"
  [ "$(cat "$cache/$target/.pin")" = "$key" ] || die "wrong cache key for tmux $version at $link"
  actual="$("$binary" -V 2>&1)" || die "cached tmux $version failed at $binary: $actual"
  [ "$actual" = "tmux $version" ] || die "wrong version at $binary: got $actual, want tmux $version"
  printf 'local-tmux: %s\n' "$actual"
}

build() {
  if [ -e "$link" ] || [ -L "$link" ]; then verify; return; fi
  if [ "$(uname -s)" = Darwin ]; then
    command -v brew >/dev/null 2>&1 || die 'Homebrew libraries are needed to build tmux; install libevent and ncurses before retrying'
    local event_prefix curses_prefix
    event_prefix="$(brew --prefix libevent 2>/dev/null)" || die 'libevent is missing; install it before retrying'
    curses_prefix="$(brew --prefix ncurses 2>/dev/null)" || die 'ncurses is missing; install it before retrying'
    export CPPFLAGS="-I$event_prefix/include -I$curses_prefix/include"
    export LDFLAGS="-L$event_prefix/lib -L$curses_prefix/lib"
    export TMUX_CONFIGURE_ARGS='--disable-utf8proc --disable-jemalloc'
  else
    unset CPPFLAGS LDFLAGS TMUX_CONFIGURE_ARGS
  fi
  mkdir -p "$cache"
  local folder
  folder="$(mktemp -d "$cache/.build.XXXXXXXX")" || die "cannot create build directory for tmux $version"
  # An interrupted build leaves only an ignored .build directory, never a key.
  if ! "$root/scripts/install-tmux.sh" "$version" "$sum" "$folder/install"; then
    rm -rf "$folder"
    die "build tmux $version failed (download, SHA-256, dependency, or compile); retry when available"
  fi
  actual="$("$folder/install/bin/tmux" -V 2>&1)" || {
    rm -rf "$folder"
    die "built tmux $version failed its version check: $actual"
  }
  if [ "$actual" != "tmux $version" ]; then
    rm -rf "$folder"
    die "built tmux has wrong version: got $actual, want tmux $version"
  fi
  printf '%s\n' "$key" > "$folder/.pin"
  # -n refuses an existing symlink to a directory instead of creating a link
  # inside the winner's build (BSD and GNU ln both support it).
  if ln -s -n "${folder##*/}" "$link" 2>/dev/null; then
    verify
  else
    rm -rf "$folder"
    verify # another gate published the same key, or this fails clearly
  fi
}

script_reaches_tmux() {
  local base="$1" merge files
  merge="$(git -C "$root" merge-base "$base" HEAD 2>/dev/null)" || return 0
  files="$(git -C "$root" diff --name-only "$merge"; git -C "$root" ls-files --others --exclude-standard)" || return 0
  case "$files" in
    *Makefile*|*tool-versions.env*|*scripts/local-tmux.sh*|*scripts/install-tmux.sh*|*.githooks/pre-push*|*.github/workflows/ci.yml*|*.github/workflows/macos.yml*) return 0 ;;
  esac
  return 1
}

reaches_tmux() {
  local pkgs pkg test_imports
  local -a selected=()
  pkgs="$("$root/scripts/changed-go-packages.sh" "$1")" || die 'cannot find changed Go packages'
  case "$pkgs" in *./internal/agent/tmux*|*./...*) return 0 ;; esac
  while IFS= read -r pkg; do
    [ -n "$pkg" ] && selected+=("$pkg")
  done <<< "$pkgs"
  if [ "${#selected[@]}" -gt 0 ]; then
    test_imports="$(cd "$root" && go list -f '{{range .TestImports}}{{.}}{{"\n"}}{{end}}{{range .XTestImports}}{{.}}{{"\n"}}{{end}}' "${selected[@]}")" || die 'cannot find selected package test imports'
    case $'\n'"$test_imports"$'\n' in
      *$'\n'github.com/linuxdynasty/fleet-board/internal/agent/tmux/tmuxtest$'\n'*) return 0 ;;
    esac
  fi
  script_reaches_tmux "$1"
}

case "${1:-}" in
  path)
    [ "$#" -eq 2 ] || die 'usage: local-tmux.sh path 32|latest'
    pin "$2"
    printf '%s\n' "$binary"
    ;;
  prepare)
    [ "$#" -eq 2 ] || die 'usage: local-tmux.sh prepare BASE_REF'
    if [ "${FLEET_REQUIRE_TMUX:-}" = 1 ]; then
      pin 32
      [ "${FLEET_TMUX_32:-}" = "$binary" ] || die 'gate FLEET_TMUX_32 does not name the pinned cache path'
      pin latest
      [ "${FLEET_TMUX_LATEST:-}" = "$binary" ] || die 'gate FLEET_TMUX_LATEST does not name the pinned cache path'
    fi
    if reaches_tmux "$2"; then
      for choice in 32 latest; do pin "$choice"; build; done
    else
      printf 'local-tmux: no tmux package reached; pinned builds are not needed\n'
    fi
    ;;
  extra-tests)
    [ "$#" -eq 2 ] || die 'usage: local-tmux.sh extra-tests BASE_REF'
    pkgs="$("$root/scripts/changed-go-packages.sh" "$2")" || die 'cannot find changed Go packages'
    case "$pkgs" in *./internal/agent/tmux*|*./...*) exit 0 ;; esac
    script_reaches_tmux "$2" || exit 0
    [ "${FLEET_REQUIRE_TMUX:-}" = 1 ] && [ -n "${FLEET_TMUX_32:-}" ] && [ -n "${FLEET_TMUX_LATEST:-}" ] || die 'extra tmux tests need both gate pins and FLEET_REQUIRE_TMUX=1'
    case "${TEST_COUNT:-}" in ''|*[!0-9]*|0*) die 'TEST_COUNT must be a positive integer' ;; esac
    [ "${TEST_TIMEOUT:-}" = 30m ] || die 'extra tmux tests need the gate timeout (30m)'
    printf 'local-tmux: gate script changed; testing both pins in tmux packages\n'
    CGO_ENABLED=0 go test -timeout "$TEST_TIMEOUT" -shuffle=on -count="$TEST_COUNT" ./internal/agent/tmux/...
    race_cgo=1
    [ "$(uname -s)" = Darwin ] && race_cgo=0
    CGO_ENABLED="$race_cgo" go test -timeout "$TEST_TIMEOUT" -race -shuffle=on -count=1 ./internal/agent/tmux/...
    ;;
  *) die 'usage: local-tmux.sh path 32|latest | prepare BASE_REF | extra-tests BASE_REF' ;;
esac
