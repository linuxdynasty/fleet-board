package wakenet

import (
	"testing"

	"golang.org/x/sys/unix"
)

// TestDarwinConstants checks the PF_ROUTE numbers routesock.go hard-codes.
func TestDarwinConstants(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		got, want int
	}{
		{"sizeof rt_msghdr", rtMsghdrLen, unix.SizeofRtMsghdr},
		{"RTM_VERSION", rtmVersion, unix.RTM_VERSION},
		{"RTM_ADD", rtmAdd, unix.RTM_ADD},
		{"RTM_DELETE", rtmDelete, unix.RTM_DELETE},
		{"RTM_CHANGE", rtmChange, unix.RTM_CHANGE},
		{"RTM_GET", rtmGet, unix.RTM_GET},
		{"RTM_MISS", rtmMiss, unix.RTM_MISS},
		{"RTM_NEWADDR", rtmNewAddr, unix.RTM_NEWADDR},
		{"RTM_DELADDR", rtmDelAddr, unix.RTM_DELADDR},
		{"RTM_IFINFO", rtmIfinfo, unix.RTM_IFINFO},
		{"RTM_IFINFO2", rtmIfinfo2, unix.RTM_IFINFO2},
		{"RTA_DST", rtaDstBit, unix.RTA_DST},
		{"RTA_GATEWAY", rtaGatewayBit, unix.RTA_GATEWAY},
		{"RTA_NETMASK", rtaNetmaskBit, unix.RTA_NETMASK},
		{"RTAX_MAX", rtaxMax, unix.RTAX_MAX},
		{"RTF_UP", rtfUp, unix.RTF_UP},
		{"RTF_GATEWAY", rtfGateway, unix.RTF_GATEWAY},
		{"RTF_HOST", rtfHost, unix.RTF_HOST},
		{"RTF_IFSCOPE", rtfIfscope, unix.RTF_IFSCOPE},
		{"AF_INET", afInet, unix.AF_INET},
		{"AF_INET6", afInet6Darwin, unix.AF_INET6},
		{"AF_LINK", afLinkDarwin, unix.AF_LINK},
		{"CLOCK_MONOTONIC_RAW", int(clockWithSleep), unix.CLOCK_MONOTONIC_RAW},
		{"CLOCK_UPTIME_RAW", int(clockAwake), unix.CLOCK_UPTIME_RAW},
	}
	for _, tc := range tests {
		if tc.got != tc.want {
			t.Errorf("%s = %d, x/sys says %d", tc.name, tc.got, tc.want)
		}
	}
}
