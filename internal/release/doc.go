// Package release is how fleet-board ships and updates itself.
//
// GitHub Releases holds one archive per program and platform plus one
// SHA256SUMS file (02j). The install script and the `upgrade` commands all
// verify a download against that file before anything is installed: a
// checksum mismatch installs nothing. Databases migrate forward only when
// the new binary starts; a downgrade is refused before any download.
package release
