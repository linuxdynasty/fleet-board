package main

import (
	"fmt"
	"os"
	"os/exec"
	"time"

	"golang.org/x/sys/unix"
)

// readSleepMark reads kern.waketime; it is zero until the first wake since boot.
func readSleepMark() (SleepMark, error) {
	tv, err := unix.SysctlTimeval("kern.waketime")
	if err != nil {
		return SleepMark{}, fmt.Errorf("sysctl kern.waketime: %w", err)
	}
	if tv.Sec == 0 && tv.Usec == 0 {
		return SleepMark{}, nil
	}
	return SleepMark{WakeTime: time.Unix(tv.Sec, int64(tv.Usec)*1000)}, nil
}

// kernelWakeTime is the last wake time when the kernel records one.
func kernelWakeTime() (time.Time, bool) {
	m, err := readSleepMark()
	if err != nil || m.WakeTime.IsZero() {
		return time.Time{}, false
	}
	return m.WakeTime, true
}

// readDefaultRoute on macOS needs an exec of route(8), so it runs only when the
// -route-exec flag asks for it; its cost is measured separately.
func readDefaultRoute(useExec bool) (DefaultRoute, bool) {
	if !useExec {
		return DefaultRoute{}, false
	}
	out, err := exec.Command("/sbin/route", "-n", "get", "default").CombinedOutput()
	if err != nil && len(out) == 0 {
		return DefaultRoute{}, false
	}
	return parseRouteGetDefault(string(out)), true
}

// openPush opens a PF_ROUTE socket; any user may read it, no root needed.
func openPush() (*os.File, func([]byte) []PushEvent, error) {
	fd, err := unix.Socket(unix.AF_ROUTE, unix.SOCK_RAW, unix.AF_UNSPEC)
	if err != nil {
		return nil, nil, fmt.Errorf("open routing socket: %w", err)
	}
	unix.CloseOnExec(fd)
	if err := unix.SetNonblock(fd, true); err != nil {
		_ = unix.Close(fd)
		return nil, nil, fmt.Errorf("nonblock routing socket: %w", err)
	}
	return os.NewFile(uintptr(fd), "route"), parseDarwinRouteMsgs, nil
}
