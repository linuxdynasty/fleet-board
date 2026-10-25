package release

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"strings"
)

// Programs shipped in every release.
const (
	ProgramFleetd = "fleetd"
	ProgramAgent  = "fleet-agent"
	ProgramFleet  = "fleet"
)

// ChecksumName is the one checksum file published beside the archives.
const ChecksumName = "SHA256SUMS"

// DefaultRepo is the repository releases are fetched from.
const DefaultRepo = "linuxdynasty/fleet-board"

// AssetName is the archive file for one program on one platform:
// "<program>-<goos>-<goarch>.tar.gz".
func AssetName(program, goos, goarch string) string {
	return fmt.Sprintf("%s-%s-%s.tar.gz", program, goos, goarch)
}

// ParseChecksums reads a SHA256SUMS file: lines of "<64 hex>  <name>".
// It refuses an empty file, a malformed line, an absolute or escaping name,
// and a name listed twice. Names are data, never paths: callers join only
// the exact asset name they selected.
func ParseChecksums(data []byte) (map[string]string, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("release: checksum file is empty")
	}
	if len(data) > 1<<20 {
		return nil, fmt.Errorf("release: checksum file is too large (%d bytes)", len(data))
	}
	out := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		hash, name, ok := strings.Cut(line, " ")
		if !ok {
			return nil, fmt.Errorf("release: bad checksum line %q", shortLine(line))
		}
		name = strings.TrimLeft(name, " \t*")
		if len(hash) != 64 || !isHex(hash) {
			return nil, fmt.Errorf("release: bad checksum for %q", shortLine(name))
		}
		if err := checkName(name); err != nil {
			return nil, err
		}
		if _, dup := out[name]; dup {
			return nil, fmt.Errorf("release: checksum file lists %q twice", name)
		}
		out[name] = strings.ToLower(hash)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("release: checksum file holds no entries")
	}
	return out, nil
}

func checkName(name string) error {
	if name == "" || name != strings.TrimSpace(name) {
		return fmt.Errorf("release: bad file name in checksum file")
	}
	if strings.HasPrefix(name, "/") || strings.Contains(name, "\\") {
		return fmt.Errorf("release: bad file name %q in checksum file", name)
	}
	for part := range strings.SplitSeq(name, "/") {
		if part == "" || part == "." || part == ".." {
			return fmt.Errorf("release: bad file name %q in checksum file", name)
		}
	}
	return nil
}

func shortLine(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 60 {
		return s[:60] + "…"
	}
	return s
}

func isHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F' {
			continue
		}
		return false
	}
	return true
}

// VerifyBytes reports whether data hashes to wantHex. A mismatch is an
// error; nothing is written anywhere.
func VerifyBytes(data []byte, wantHex string) error {
	wantHex = strings.ToLower(strings.TrimSpace(wantHex))
	want, err := hex.DecodeString(wantHex)
	if err != nil || len(want) != sha256.Size {
		return fmt.Errorf("release: bad checksum %q", shortLine(wantHex))
	}
	sum := sha256.Sum256(data)
	if subtle.ConstantTimeCompare(sum[:], want) != 1 {
		return fmt.Errorf("release: checksum mismatch: the download is not what the release published")
	}
	return nil
}

// Asset is one downloadable release file.
type Asset struct {
	Name string
	URL  string
}

// SelectAsset picks the archive for one program on one platform out of a
// release's files. Anything else in the release is ignored.
func SelectAsset(assets []Asset, program, goos, goarch string) (Asset, error) {
	want := AssetName(program, goos, goarch)
	for _, a := range assets {
		if a.Name == want {
			if a.URL == "" {
				return Asset{}, fmt.Errorf("release: %s has no download address", want)
			}
			return a, nil
		}
	}
	return Asset{}, fmt.Errorf("release: %s is not in this release", want)
}

// CompareVersions orders two release versions ("v1.2.3", leading v optional):
// -1 when a is older, 0 when equal, +1 when a is newer. Four-part or
// non-numeric versions are refused.
func CompareVersions(a, b string) (int, error) {
	pa, err := parseVersion(a)
	if err != nil {
		return 0, err
	}
	pb, err := parseVersion(b)
	if err != nil {
		return 0, err
	}
	for i := range 3 {
		switch {
		case pa[i] < pb[i]:
			return -1, nil
		case pa[i] > pb[i]:
			return 1, nil
		}
	}
	return 0, nil
}

func parseVersion(v string) ([3]int, error) {
	var out [3]int
	s := strings.TrimSpace(v)
	s = strings.TrimPrefix(s, "v")
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return out, fmt.Errorf("release: bad version %q: want vX.Y.Z", v)
	}
	for i, p := range parts {
		if p == "" {
			return out, fmt.Errorf("release: bad version %q: want vX.Y.Z", v)
		}
		n := 0
		for j := 0; j < len(p); j++ {
			c := p[j]
			if c < '0' || c > '9' {
				return out, fmt.Errorf("release: bad version %q: want vX.Y.Z", v)
			}
			n = n*10 + int(c-'0')
			if n > 1000000 {
				return out, fmt.Errorf("release: bad version %q: number too large", v)
			}
		}
		out[i] = n
	}
	return out, nil
}
