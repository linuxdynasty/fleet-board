package main

import (
	"testing"

	"golang.org/x/sys/unix"
)

func TestDarwinConstantsMatchKernel(t *testing.T) {
	t.Parallel()
	want := map[int]string{
		unix.RTM_ADD: "RTM_ADD", unix.RTM_DELETE: "RTM_DELETE", unix.RTM_CHANGE: "RTM_CHANGE",
		unix.RTM_NEWADDR: "RTM_NEWADDR", unix.RTM_DELADDR: "RTM_DELADDR",
		unix.RTM_IFINFO: "RTM_IFINFO", unix.RTM_IFINFO2: "RTM_IFINFO2",
	}
	for v, name := range want {
		if got := darwinRTMNames[byte(v)]; got != name {
			t.Errorf("0x%x: got %q, want %q", v, got, name)
		}
	}
	if darwinSizeofRtMsghdr != unix.SizeofRtMsghdr {
		t.Errorf("rt_msghdr size %d, kernel %d", darwinSizeofRtMsghdr, unix.SizeofRtMsghdr)
	}
	if flagsUpRunning != unix.IFF_UP|unix.IFF_RUNNING {
		t.Errorf("IFF_UP|IFF_RUNNING = 0x%x, want 0x%x", flagsUpRunning, unix.IFF_UP|unix.IFF_RUNNING)
	}
}
