//go:build !darwin && !linux

package machine

import "errors"

// errUnsupport is what a reading says on a system the agent is not built for
// (it builds for darwin and linux only).
var errUnsupport = errors.New("machine: not supported on this system")
