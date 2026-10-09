package wakenet

import (
	"fmt"
	"io"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// Linux: CLOCK_BOOTTIME counts suspended time, CLOCK_MONOTONIC does not.
// ADR 0029.
const (
	clockWithSleep = unix.CLOCK_BOOTTIME
	clockAwake     = unix.CLOCK_MONOTONIC
)

var pushHint = netlinkHint

// defaultRoutes dumps the routing table over netlink (RTM_GETROUTE, the data
// `ip route` reads) and picks the default routes from it.
func defaultRoutes(names map[int]string) (v4, v6 Route, err error) {
	b, err := syscall.NetlinkRIB(syscall.RTM_GETROUTE, syscall.AF_UNSPEC)
	if err != nil {
		return Route{}, Route{}, fmt.Errorf("routing table dump: %w", err)
	}
	return netlinkDefaultRoutes(b, names)
}

// openPush opens a NETLINK_ROUTE socket subscribed to the link, address and
// route groups; receiving these multicast groups needs no privilege.
func openPush() (io.ReadCloser, error) {
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW|unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK, unix.NETLINK_ROUTE)
	if err != nil {
		return nil, fmt.Errorf("open netlink socket: %w", err)
	}
	groups := uint32(unix.RTMGRP_LINK | unix.RTMGRP_IPV4_IFADDR | unix.RTMGRP_IPV6_IFADDR |
		unix.RTMGRP_IPV4_ROUTE | unix.RTMGRP_IPV6_ROUTE)
	if err := unix.Bind(fd, &unix.SockaddrNetlink{Family: unix.AF_NETLINK, Groups: groups}); err != nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("bind netlink groups: %w", err)
	}
	return os.NewFile(uintptr(fd), "netlink"), nil
}
