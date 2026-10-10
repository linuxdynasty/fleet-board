package machine

import "fmt"

// platformOSVersion is "<ID> <VERSION_ID>" from /etc/os-release.
func platformOSVersion() (string, error) {
	b, err := readSmall("/etc/os-release")
	if err != nil {
		b, err = readSmall("/usr/lib/os-release")
	}
	if err != nil {
		return "", fmt.Errorf("os-release: %w", err)
	}
	return parseOSRelease(string(b))
}
