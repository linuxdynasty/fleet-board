package main

import (
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// readSleepMark reads CLOCK_BOOTTIME minus CLOCK_MONOTONIC: BOOTTIME counts
// suspended time and MONOTONIC does not, so the difference grows on resume.
func readSleepMark() (SleepMark, error) {
	var boot, mono unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_BOOTTIME, &boot); err != nil {
		return SleepMark{}, fmt.Errorf("clock_gettime BOOTTIME: %w", err)
	}
	if err := unix.ClockGettime(unix.CLOCK_MONOTONIC, &mono); err != nil {
		return SleepMark{}, fmt.Errorf("clock_gettime MONOTONIC: %w", err)
	}
	return SleepMark{Suspended: time.Duration(boot.Nano() - mono.Nano())}, nil
}

// kernelWakeTime: Linux keeps no wake timestamp a process can read; the owner
// compares against the journal's "PM: suspend exit" line instead.
func kernelWakeTime() (time.Time, bool) { return time.Time{}, false }

// readDefaultRoute reads /proc/net/route, a cheap file read, so the flag is ignored.
func readDefaultRoute(bool) (DefaultRoute, bool) {
	f, err := os.Open("/proc/net/route")
	if err != nil {
		return DefaultRoute{}, false
	}
	defer func() { _ = f.Close() }()
	return parseProcNetRoute(f), true
}

// openPush opens a NETLINK_ROUTE socket subscribed to link, address and route
// groups; receiving these multicast groups needs no privilege.
func openPush() (*os.File, func([]byte) []PushEvent, error) {
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW|unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK, unix.NETLINK_ROUTE)
	if err != nil {
		return nil, nil, fmt.Errorf("open netlink socket: %w", err)
	}
	groups := uint32(unix.RTMGRP_LINK | unix.RTMGRP_IPV4_IFADDR | unix.RTMGRP_IPV6_IFADDR |
		unix.RTMGRP_IPV4_ROUTE | unix.RTMGRP_IPV6_ROUTE)
	if err := unix.Bind(fd, &unix.SockaddrNetlink{Family: unix.AF_NETLINK, Groups: groups}); err != nil {
		_ = unix.Close(fd)
		return nil, nil, fmt.Errorf("bind netlink groups: %w", err)
	}
	return os.NewFile(uintptr(fd), "netlink"), parseNetlinkMsgs, nil
}
