//go:build !darwin && !linux

package machine

import "context"

// platformPressure: the agent builds for darwin and linux only; any other
// system has no memory pressure level.
func platformPressure(context.Context, uint64, uint64, bool) (uint32, error) {
	return 0, errUnsupport
}
