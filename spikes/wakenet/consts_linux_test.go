package main

import (
	"testing"

	"golang.org/x/sys/unix"
)

func TestLinuxConstantsMatchKernel(t *testing.T) {
	t.Parallel()
	want := map[int]string{
		unix.RTM_NEWLINK: "RTM_NEWLINK", unix.RTM_DELLINK: "RTM_DELLINK",
		unix.RTM_NEWADDR: "RTM_NEWADDR", unix.RTM_DELADDR: "RTM_DELADDR",
		unix.RTM_NEWROUTE: "RTM_NEWROUTE", unix.RTM_DELROUTE: "RTM_DELROUTE",
	}
	for v, name := range want {
		if got := netlinkRTMNames[uint16(v)]; got != name {
			t.Errorf("%d: got %q, want %q", v, got, name)
		}
	}
	if nlmsgHdrLen != unix.NLMSG_HDRLEN || unix.RT_TABLE_MAIN != 254 {
		t.Errorf("netlink header %d (kernel %d), main table %d", nlmsgHdrLen, unix.NLMSG_HDRLEN, unix.RT_TABLE_MAIN)
	}
	if flagsUpRunning != unix.IFF_UP|unix.IFF_RUNNING {
		t.Errorf("IFF_UP|IFF_RUNNING = 0x%x", unix.IFF_UP|unix.IFF_RUNNING)
	}
}
