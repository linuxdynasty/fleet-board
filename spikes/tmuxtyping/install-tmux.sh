#!/bin/sh
# install-tmux.sh VERSION SHA256 PREFIX
#
# Builds one tmux release from the official tarball into PREFIX after checking
# its SHA-256. Proved on ubuntu:22.04 (linux/amd64) for 3.2a and 3.7c; see
# results/README.md. Build dependencies on Ubuntu:
#   sudo apt-get install -y --no-install-recommends \
#     build-essential pkg-config bison libevent-dev libncurses-dev curl ca-certificates
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
echo "$sum  $tmp/tmux.tar.gz" | sha256sum -c -
tar xzf "$tmp/tmux.tar.gz" -C "$tmp"
cd "$tmp/tmux-$version"
./configure --prefix="$prefix" >"$tmp/configure.log" 2>&1 || {
	tail -40 "$tmp/configure.log" >&2
	exit 1
}
make -j"$(nproc)" >"$tmp/make.log" 2>&1 || {
	tail -40 "$tmp/make.log" >&2
	exit 1
}
make install >/dev/null
"$prefix/bin/tmux" -V
