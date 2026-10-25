#!/bin/sh
# install.sh: install fleetd, fleet-agent or fleet from GitHub Releases (02j).
#
# One line (the "Add machine" command shows it):
#   curl -fsSL https://raw.githubusercontent.com/linuxdynasty/fleet-board/main/packaging/install.sh |
#     sh -s -- --program fleet-agent
#
# It downloads the archive for this machine, checks it against SHA256SUMS,
# installs it into ~/.local/bin and installs the service unit. A checksum
# mismatch installs nothing. macOS owners clear the quarantine flag (printed
# every run); Linux owners run `sudo loginctl enable-linger <user>` once.
#
# Flags: --program NAME (fleetd, fleet-agent or fleet; required),
#   --version TAG (default: the latest release), --repo OWNER/NAME,
#   --os darwin|linux, --arch amd64|arm64 (default: detected),
#   --no-start (install the unit without enabling it), --help.
#
# Test hooks (advanced use only): FLEET_RELEASE_BASE (default
# https://github.com/<repo>/releases/download), FLEET_UNIT_BASE (default
# https://raw.githubusercontent.com/<repo>/<tag>/packaging) and
# FLEET_API_BASE (default https://api.github.com). HOME sets the install
# target. Portable to the macOS system sh and dash; needs only curl, tar
# and shasum (or sha256sum).
set -eu

program=""
version=""
repo="linuxdynasty/fleet-board"
os=""
arch=""
start="yes"

usage() {
  echo "usage: install.sh --program fleetd|fleet-agent|fleet [--version TAG] [--repo OWNER/NAME] [--os darwin|linux] [--arch amd64|arm64] [--no-start]" >&2
}

while [ $# -gt 0 ]; do
  case "$1" in
    --program) program="${2:-}"; shift 2 ;;
    --version) version="${2:-}"; shift 2 ;;
    --repo) repo="${2:-}"; shift 2 ;;
    --os) os="${2:-}"; shift 2 ;;
    --arch) arch="${2:-}"; shift 2 ;;
    --no-start) start="no"; shift ;;
    --help|-h) usage; exit 0 ;;
    *) echo "install.sh: unknown argument '$1'" >&2; usage; exit 2 ;;
  esac
done

case "$program" in
  fleetd|fleet-agent|fleet) ;;
  "") echo "install.sh: --program is required (fleetd, fleet-agent or fleet)" >&2; usage; exit 2 ;;
  *) echo "install.sh: unknown program '$program' (fleetd, fleet-agent or fleet)" >&2; usage; exit 2 ;;
esac

command -v curl >/dev/null || { echo "install.sh: curl is required" >&2; exit 1; }
command -v tar >/dev/null || { echo "install.sh: tar is required" >&2; exit 1; }

if [ -z "$os" ]; then
  case "$(uname -s)" in
    Darwin) os="darwin" ;;
    Linux) os="linux" ;;
    *) echo "install.sh: unsupported system '$(uname -s)'; pass --os darwin|linux" >&2; exit 1 ;;
  esac
fi
if [ -z "$arch" ]; then
  case "$(uname -m)" in
    x86_64) arch="amd64" ;;
    arm64|aarch64) arch="arm64" ;;
    *) echo "install.sh: unsupported CPU '$(uname -m)'; pass --arch amd64|arm64" >&2; exit 1 ;;
  esac
fi

api_base="${FLEET_API_BASE:-https://api.github.com}"
if [ -z "$version" ]; then
  tag_json="$(curl -fsSL "$api_base/repos/$repo/releases/latest")" || {
    echo "install.sh: could not list releases for $repo" >&2; exit 1; }
  version="$(printf '%s' "$tag_json" | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1)"
  [ -n "$version" ] || { echo "install.sh: the release list has no tag" >&2; exit 1; }
fi

release_base="${FLEET_RELEASE_BASE:-https://github.com/$repo/releases/download}"
unit_base="${FLEET_UNIT_BASE:-https://raw.githubusercontent.com/$repo/$version/packaging}"
asset="$program-$os-$arch.tar.gz"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT INT TERM

echo "install.sh: fetching $program $version for $os/$arch"
curl -fsSL -o "$work/$asset" "$release_base/$version/$asset" || {
  echo "install.sh: no $asset in $version" >&2; exit 1; }
curl -fsSL -o "$work/SHA256SUMS" "$release_base/$version/SHA256SUMS" || {
  echo "install.sh: no SHA256SUMS in $version" >&2; exit 1; }

# Verify before anything is installed: a mismatch installs nothing.
want="$(grep -F " $asset" "$work/SHA256SUMS" | awk '{print $1}' || true)"
case "$want" in
  *[!0-9a-fA-F]*|"") echo "install.sh: $asset is not listed in SHA256SUMS; installs nothing" >&2; exit 1 ;;
esac
if [ "${#want}" -ne 64 ]; then
  echo "install.sh: SHA256SUMS holds a bad checksum for $asset; installs nothing" >&2; exit 1
fi
if command -v shasum >/dev/null; then
  got="$(shasum -a 256 "$work/$asset" | awk '{print $1}')"
elif command -v sha256sum >/dev/null; then
  got="$(sha256sum "$work/$asset" | awk '{print $1}')"
else
  echo "install.sh: need shasum or sha256sum to check the download; installs nothing" >&2; exit 1
fi
if [ "$got" != "$want" ]; then
  echo "install.sh: checksum mismatch for $asset: the download is not what the release published; installs nothing" >&2
  exit 1
fi

# The archive holds exactly one file, named for the program.
entries="$(tar -tzf "$work/$asset")"
if [ "$entries" != "$program" ]; then
  echo "install.sh: the archive holds '$entries', want one file named '$program'; installs nothing" >&2; exit 1
fi
tar -xzf "$work/$asset" -C "$work"

bin_dir="$HOME/.local/bin"
mkdir -p "$bin_dir"
cp "$work/$program" "$bin_dir/$program"
chmod 755 "$bin_dir/$program"
echo "install.sh: installed $bin_dir/$program"

if [ "$os" = "darwin" ]; then
  xattr -d com.apple.quarantine "$bin_dir/$program" 2>/dev/null || true
  echo "install.sh: macOS marks downloads as quarantined; cleared with:"
  echo "  xattr -d com.apple.quarantine $bin_dir/$program"
fi

# Service units. `fleet` is a CLI with no unit.
if [ "$program" = "fleet" ]; then
  echo "install.sh: done (fleet has no service unit)"
  exit 0
fi

if [ "$os" = "darwin" ]; then
  unit_dir="$HOME/Library/LaunchAgents"
  unit="com.fleet-board.$program.plist"
  mkdir -p "$unit_dir"
  curl -fsSL -o "$work/$unit" "$unit_base/launchd/$unit" || {
    echo "install.sh: no unit $unit for $version" >&2; exit 1; }
  home_esc=$(printf '%s' "$HOME" | sed 's/[&|\\]/\\&/g')
  sed "s|@HOME@|$home_esc|g" "$work/$unit" > "$unit_dir/$unit"
  echo "install.sh: installed $unit_dir/$unit"
  if [ "$start" = "yes" ]; then
    launchctl unload "$unit_dir/$unit" 2>/dev/null || true
    if launchctl load "$unit_dir/$unit" 2>/dev/null || launchctl bootstrap "gui/$(id -u)" "$unit_dir/$unit" 2>/dev/null; then
      echo "install.sh: started $unit"
    else
      echo "install.sh: could not start $unit; start it with: launchctl load $unit_dir/$unit" >&2
    fi
  fi
else
  unit_dir="$HOME/.config/systemd/user"
  unit="$program.service"
  mkdir -p "$unit_dir"
  curl -fsSL -o "$work/$unit" "$unit_base/systemd/$unit" || {
    echo "install.sh: no unit $unit for $version" >&2; exit 1; }
  home_esc=$(printf '%s' "$HOME" | sed 's/[&|\\]/\\&/g')
  sed "s|@HOME@|$home_esc|g" "$work/$unit" > "$unit_dir/$unit"
  echo "install.sh: installed $unit_dir/$unit"
  if [ "$start" = "yes" ]; then
    if systemctl --user daemon-reload && systemctl --user enable --now "$unit" 2>/dev/null; then
      echo "install.sh: started $unit"
    else
      echo "install.sh: could not start $unit; start it with: systemctl --user enable --now $unit" >&2
    fi
  fi
  echo "install.sh: once per worker, so the agent survives logout:"
  echo "  sudo loginctl enable-linger $(id -un)"
fi

if [ "$program" = "fleetd" ]; then
  echo "install.sh: next: fleetd init, then start the hub (or it is already running)"
else
  echo "install.sh: next: enroll this machine with the token from: fleetd enroll-token --machine NAME"
  echo "install.sh: before that, fleet-agent doctor shows the local checks"
fi
