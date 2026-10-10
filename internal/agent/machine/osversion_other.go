//go:build !darwin && !linux

package machine

// platformOSVersion: the agent builds for darwin and linux only.
func platformOSVersion() (string, error) { return "", errUnsupport }
