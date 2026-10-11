#!/bin/sh
# install-tmux.sh VERSION SHA256 PREFIX
#
# Builds one tmux release from the official tarball into PREFIX after checking
# its SHA-256 (nothing is built on a mismatch). CI uses it for the two releases
# the tmux tests run against (tool-versions.env: TMUX_32_*, TMUX_LATEST_*).
# Proved on ubuntu:22.04 (linux/amd64) for 3.2a and 3.7c in spike 0.5
# (spikes/tmuxtyping/results), and on macOS for both in row 7 PR 7c2.
#
# Build dependencies:
#   Ubuntu: sudo apt-get install -y --no-install-recommends \
#             build-essential pkg-config bison libevent-dev libncurses-dev curl ca-certificates
#   macOS:  brew install libevent ncurses pkg-config, then point the compiler at
#           them: CPPFLAGS="-I$(brew --prefix libevent)/include -I$(brew --prefix ncurses)/include"
#           LDFLAGS="-L$(brew --prefix libevent)/lib -L$(brew --prefix ncurses)/lib"
#           TMUX_CONFIGURE_ARGS="--disable-utf8proc --disable-jemalloc"
set -eu

if [ "$#" -ne 3 ]; then
	echo "usage: $0 VERSION SHA256 PREFIX" >&2
	exit 2
fi
version=$1
sum=$2
prefix=$3

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
curl -fsSL -o "$tmp/tmux.tar.gz" \
	"https://github.com/tmux/tmux/releases/download/$version/tmux-$version.tar.gz"
if command -v sha256sum >/dev/null 2>&1; then
	echo "$sum  $tmp/tmux.tar.gz" | sha256sum -c -
else
	echo "$sum  $tmp/tmux.tar.gz" | shasum -a 256 -c -
fi
tar xzf "$tmp/tmux.tar.gz" -C "$tmp"
cd "$tmp/tmux-$version"
# TMUX_CONFIGURE_ARGS adds configure flags (macOS: --disable-utf8proc and
# --disable-jemalloc, which 3.7c asks for there; 3.2a ignores them with a
# warning).
# shellcheck disable=SC2086
./configure --prefix="$prefix" ${TMUX_CONFIGURE_ARGS:-} >"$tmp/configure.log" 2>&1 || {
	tail -40 "$tmp/configure.log" >&2
	exit 1
}
jobs=$(getconf _NPROCESSORS_ONLN 2>/dev/null || echo 2)
make -j"$jobs" >"$tmp/make.log" 2>&1 || {
	tail -40 "$tmp/make.log" >&2
	exit 1
}
make install >/dev/null
"$prefix/bin/tmux" -V
