package machine

import (
	"context"
	"fmt"

	"golang.org/x/sys/unix"
)

// platformPressure reads kern.memorystatus_vm_pressure_level, one sysctl that
// needs no privilege (ADR 0023). The kernel's values are 1, 2 and 4; anything
// else is not a reading.
func platformPressure(_ context.Context, _, _ uint64, _ bool) (uint32, error) {
	v, err := unix.SysctlUint32("kern.memorystatus_vm_pressure_level")
	if err != nil {
		return 0, fmt.Errorf("memory pressure sysctl: %w", err)
	}
	switch v {
	case levelNormal, levelWarn, levelCritical:
		return v, nil
	default:
		return 0, fmt.Errorf("%w: memory pressure level %d", errParse, v)
	}
}
