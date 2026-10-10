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

// SinceBoot reads the clock that keeps counting while the machine sleeps
// and that setting the wall clock does not move (Linux CLOCK_BOOTTIME,
// macOS CLOCK_MONOTONIC_RAW): time since boot. Go's monotonic clock stops
// during sleep, so a caller measuring elapsed time takes the larger of the
// two (02i "Clocks"); internal/agent/conn does for hub time.
func SinceBoot() (time.Duration, error) { return clockRead(clockWithSleep) }

func clockRead(id int32) (time.Duration, error) {
	var ts unix.Timespec
	if err := unix.ClockGettime(id, &ts); err != nil {
		return 0, fmt.Errorf("clock_gettime: %w", err)
	}
	return time.Duration(ts.Nano()), nil
}
