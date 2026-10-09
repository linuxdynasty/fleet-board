package wakenet

import (
	"errors"
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

// macOS: CLOCK_MONOTONIC_RAW is mach_continuous_time (counts sleep),
// CLOCK_UPTIME_RAW is mach_absolute_time (does not). ADR 0029.
const (
	clockWithSleep = unix.CLOCK_MONOTONIC_RAW
	clockAwake     = unix.CLOCK_UPTIME_RAW
)

// dumpTries bounds the retries when the routing table grows between the
// sysctl that sizes the dump and the one that fills it (ENOMEM).
const dumpTries = 3

var pushHint = routeSocketHint

// defaultRoutes dumps the routing table (sysctl CTL_NET, PF_ROUTE, 0,
// AF_UNSPEC, NET_RT_DUMP, 0: the data `netstat -rn` reads) and picks the
// default routes from it. Only "net" resolves by name; the rest is numeric.
func defaultRoutes(names map[int]string) (v4, v6 Route, err error) {
	var b []byte
	for range dumpTries {
		b, err = unix.SysctlRaw("net", unix.AF_ROUTE, 0, unix.AF_UNSPEC, unix.NET_RT_DUMP, 0)
		if !errors.Is(err, unix.ENOMEM) {
			break
		}
	}
	if err != nil {
		return Route{}, Route{}, fmt.Errorf("routing table dump: %w", err)
	}
	return routeDumpDefaults(b, names)
}

// openPush opens a PF_ROUTE socket; any user may read it, no root needed.
func openPush() (io.ReadCloser, error) {
	fd, err := unix.Socket(unix.AF_ROUTE, unix.SOCK_RAW, unix.AF_UNSPEC)
	if err != nil {
		return nil, fmt.Errorf("open routing socket: %w", err)
	}
	unix.CloseOnExec(fd)
	if err := unix.SetNonblock(fd, true); err != nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("routing socket non-blocking: %w", err)
	}
	return os.NewFile(uintptr(fd), "route"), nil
}
