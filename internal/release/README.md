# internal/release

How fleet-board ships and updates itself. GitHub Releases holds one archive
per program and platform plus one `SHA256SUMS` file (02j). Everything
downloaded is checked against that file before it is installed: a checksum
mismatch installs nothing.

- `release.go`: read the checksum file, verify bytes, pick the archive for
  a program and platform, compare versions.
- `fetch.go`: list releases and download files (`GitHub`; public reads, no
  credentials). Tests use a fake endpoint.
- `upgrade.go`: the `upgrade` flow: fetch, refuse a downgrade (databases
  move forward only), verify, swap, restart.
- `swap.go`: unpack one archive and replace the binary in a single rename
  (`FileSwapper`); restart the unit through the service manager
  (`ServiceRestarter`: `launchctl` on macOS, `systemctl --user` on Linux,
  run through `internal/proc` caller `service`).

Archive names: `<program>-<goos>-<goarch>.tar.gz`, each holding one file
named for the program. Built by `scripts/release-build.sh`.

Tests: `CGO_ENABLED=0 go test ./internal/release/`. Covers a tampered
archive (swaps nothing), an upgrade from a fake endpoint, a refused
downgrade, and archives that must not install.
