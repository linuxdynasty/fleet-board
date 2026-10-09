//go:build darwin || linux

package wakenet

import (
	"fmt"
	"time"

	"golang.org/x/sys/unix"
)

// SystemAsleep returns the time asleep since boot: Linux CLOCK_BOOTTIME minus
// CLOCK_MONOTONIC, macOS CLOCK_MONOTONIC_RAW minus CLOCK_UPTIME_RAW (ADR 0029).
// Never kern.waketime, never darwin CLOCK_MONOTONIC.
func SystemAsleep() (time.Duration, error) {
	return asleepFrom(clockRead, clockWithSleep, clockAwake)
}

func clockRead(id int32) (time.Duration, error) {
	var ts unix.Timespec
	if err := unix.ClockGettime(id, &ts); err != nil {
		return 0, fmt.Errorf("clock_gettime: %w", err)
	}
	return time.Duration(ts.Nano()), nil
}
