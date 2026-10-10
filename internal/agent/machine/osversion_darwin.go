package machine

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// platformOSVersion is the macOS product version ("27.0.1") from
// kern.osproductversion. gopsutil's host package would start `sw_vers` outside
// internal/proc (01 principle 8), so it is not used.
func platformOSVersion() (string, error) {
	v, err := unix.Sysctl("kern.osproductversion")
	if err != nil {
		return "", fmt.Errorf("os version sysctl: %w", err)
	}
	if v == "" || len(v) > maxFactBytes {
		return "", fmt.Errorf("%w: os version", errParse)
	}
	for _, r := range v {
		if r != '.' && (r < '0' || r > '9') {
			return "", fmt.Errorf("%w: os version", errParse)
		}
	}
	return v, nil
}
