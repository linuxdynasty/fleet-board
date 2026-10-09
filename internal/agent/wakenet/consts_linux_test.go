package wakenet

import (
	"testing"

	"golang.org/x/sys/unix"
)

// TestLinuxConstants checks the rtnetlink numbers netlink.go hard-codes.
func TestLinuxConstants(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		got, want int
	}{
		{"sizeof nlmsghdr", nlHeaderLen, unix.SizeofNlMsghdr},
		{"sizeof rtmsg", rtmsgLen, unix.SizeofRtMsg},
		{"sizeof rtnexthop", nexthopLen, unix.SizeofRtNexthop},
		{"NLMSG_ERROR", nlmsgError, unix.NLMSG_ERROR},
		{"NLMSG_DONE", nlmsgDone, unix.NLMSG_DONE},
		{"RTM_NEWLINK", nlNewLink, unix.RTM_NEWLINK},
		{"RTM_DELLINK", nlDelLink, unix.RTM_DELLINK},
		{"RTM_NEWADDR", nlNewAddr, unix.RTM_NEWADDR},
		{"RTM_DELADDR", nlDelAddr, unix.RTM_DELADDR},
		{"RTM_NEWROUTE", nlNewRoute, unix.RTM_NEWROUTE},
		{"RTM_DELROUTE", nlDelRoute, unix.RTM_DELROUTE},
		{"RTA_GATEWAY", rtaGateway, unix.RTA_GATEWAY},
		{"RTA_OIF", rtaOif, unix.RTA_OIF},
		{"RTA_PRIORITY", rtaPriority, unix.RTA_PRIORITY},
		{"RTA_MULTIPATH", rtaMultipath, unix.RTA_MULTIPATH},
		{"RTA_TABLE", rtaTable, unix.RTA_TABLE},
		{"RT_TABLE_MAIN", rtTableMain, unix.RT_TABLE_MAIN},
		{"RTN_UNICAST", rtnUnicastT, unix.RTN_UNICAST},
		{"AF_INET", afInet, unix.AF_INET},
		{"AF_INET6", afInet6Nl, unix.AF_INET6},
		{"CLOCK_BOOTTIME", int(clockWithSleep), unix.CLOCK_BOOTTIME},
		{"CLOCK_MONOTONIC", int(clockAwake), unix.CLOCK_MONOTONIC},
	}
	for _, tc := range tests {
		if tc.got != tc.want {
			t.Errorf("%s = %d, x/sys says %d", tc.name, tc.got, tc.want)
		}
	}
}
