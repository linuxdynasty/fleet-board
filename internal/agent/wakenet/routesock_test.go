package wakenet

import (
	"encoding/binary"
	"errors"
	"slices"
	"testing"
)

// PF_ROUTE builders, from macOS net/route.h and net/if.h.

// sa4 is a struct sockaddr_in; sa6 a struct sockaddr_in6; saLink a minimal
// struct sockaddr_dl. Each is padded to 4 bytes as the kernel does.
func sa4(a, b, c, d byte) []byte { return []byte{16, afInet, 0, 0, a, b, c, d, 0, 0, 0, 0, 0, 0, 0, 0} }

func sa6(ip string) []byte {
	b := make([]byte, 28)
	b[0], b[1] = 28, afInet6Darwin
	copy(b[8:], addr(ip).AsSlice())
	return b
}

func saLink(index uint16) []byte {
	b := make([]byte, 20)
	b[0], b[1] = 20, afLinkDarwin
	binary.NativeEndian.PutUint16(b[2:], index)
	return b
}

// saMaskZero is the truncated all-zero netmask macOS writes for a default route.
var saMaskZero = []byte{0, 0, 0, 0}

// rtMsg is a struct rt_msghdr followed by sockaddrs in RTA_* bit order.
func rtMsg(typ byte, index uint16, flags uint32, addrs uint32, sas ...[]byte) []byte {
	hdr := make([]byte, rtMsghdrLen)
	hdr[2], hdr[3] = rtmVersion, typ
	binary.NativeEndian.PutUint16(hdr[rtmOffIndex:], index)
	binary.NativeEndian.PutUint32(hdr[rtmOffFlags:], flags)
	binary.NativeEndian.PutUint32(hdr[rtmOffAddrs:], addrs)
	b := cat(hdr, cat(sas...))
	put16(b[0:], len(b))
	return b
}

// failed sets rtm_errno: the kernel's echo of a route request that failed.
func failed(m []byte) []byte {
	b := slices.Clone(m)
	binary.NativeEndian.PutUint32(b[rtmOffErrno:], 17) // EEXIST
	return b
}

// ifMsg is a struct if_msghdr or ifa_msghdr: same leading fields, index at 12.
func ifMsg(typ byte, index uint16, size int) []byte {
	b := make([]byte, size)
	put16(b[0:], size)
	b[2], b[3] = rtmVersion, typ
	binary.NativeEndian.PutUint16(b[12:], index)
	return b
}

const (
	dstGwMask = rtaDstBit | rtaGatewayBit | rtaNetmaskBit
	upGateway = rtfUp | rtfGateway
)

func TestRouteSocketHint(t *testing.T) {
	t.Parallel()
	defaultAdd := rtMsg(rtmAdd, 7, upGateway, dstGwMask, sa4(0, 0, 0, 0), sa4(192, 168, 1, 1), saMaskZero)
	hostAdd := rtMsg(rtmAdd, 7, rtfUp|rtfHost, rtaDstBit|rtaGatewayBit, sa4(10, 0, 0, 5), saLink(7))
	tests := []struct {
		name string
		b    []byte
		want bool
	}{
		{"interface info (utun10 down, as on the MacBook)", ifMsg(rtmIfinfo, 36, 112), true},
		{"address removed", ifMsg(rtmDelAddr, 36, 20), true},
		{"address added", ifMsg(rtmNewAddr, 36, 20), true},
		{"default route added", defaultAdd, true},
		{"default IPv6 route deleted", rtMsg(rtmDelete, 7, upGateway, dstGwMask, sa6("::"), sa6("fe80::1"), saMaskZero), true},
		{"host route is no hint", hostAdd, false},
		{"route lookup miss (the Mac mini noise) is no hint", rtMsg(rtmMiss, 0, 0, rtaDstBit, sa4(192, 0, 2, 1)), false},
		{"route lookup echo is no hint", rtMsg(rtmGet, 0, 0, rtaDstBit, sa4(192, 0, 2, 1)), false},
		{"default among others in one read", cat(hostAdd, defaultAdd), true},
		{"failed default route add is no hint", failed(defaultAdd), false},
		{"empty read", nil, false},
		{"short header is a hint", []byte{3, 0, 5}, true},
		{"length past the buffer is a hint", defaultAdd[:40], true},
		{"route message shorter than its header is a hint", ifMsg(rtmAdd, 7, 20), true},
		{"sockaddr past the message is a hint", rtMsg(rtmAdd, 7, upGateway, rtaDstBit, []byte{16, afInet, 0, 0}), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := routeSocketHint(tc.b); got != tc.want {
				t.Errorf("routeSocketHint = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRouteDumpDefaults(t *testing.T) {
	t.Parallel()
	names := map[int]string{1: "lo0", 7: "en0", 22: "utun4"}
	ifscoped := uint32(upGateway | rtfIfscope)
	tests := []struct {
		name    string
		dump    []byte
		v4, v6  Route
		wantErr bool
	}{
		{
			name: "Wi-Fi IPv4 and IPv6 defaults",
			dump: cat(
				rtMsg(rtmGet, 1, rtfUp|rtfHost, rtaDstBit|rtaGatewayBit, sa4(127, 0, 0, 1), sa4(127, 0, 0, 1)),
				rtMsg(rtmGet, 7, upGateway, dstGwMask, sa4(0, 0, 0, 0), sa4(192, 168, 1, 1), saMaskZero),
				rtMsg(rtmGet, 7, upGateway, dstGwMask, sa6("::"), sa6("fe80:7::1"), saMaskZero),
			),
			v4: Route{Iface: "en0", Gateway: addr("192.168.1.1")},
			v6: Route{Iface: "en0", Gateway: addr("fe80::1")},
		},
		{
			name: "a failed route is skipped",
			dump: cat(
				failed(rtMsg(rtmGet, 22, upGateway, dstGwMask, sa4(0, 0, 0, 0), sa4(10, 8, 0, 1), saMaskZero)),
				rtMsg(rtmGet, 7, upGateway, dstGwMask, sa4(0, 0, 0, 0), sa4(192, 168, 1, 1), saMaskZero),
			),
			v4: Route{Iface: "en0", Gateway: addr("192.168.1.1")},
		},
		{
			name: "interface-scoped defaults are skipped",
			dump: cat(
				rtMsg(rtmGet, 22, ifscoped, dstGwMask, sa6("::"), sa6("fe80:16::1"), saMaskZero),
				rtMsg(rtmGet, 7, upGateway, dstGwMask, sa4(0, 0, 0, 0), sa4(192, 168, 1, 1), saMaskZero),
				rtMsg(rtmGet, 22, ifscoped, dstGwMask, sa4(0, 0, 0, 0), sa4(10, 8, 0, 1), saMaskZero),
			),
			v4: Route{Iface: "en0", Gateway: addr("192.168.1.1")},
		},
		{
			name: "VPN default through a link address has no gateway",
			dump: rtMsg(rtmGet, 22, rtfUp, dstGwMask, sa4(0, 0, 0, 0), saLink(22), saMaskZero),
			v4:   Route{Iface: "utun4"},
		},
		{
			name: "a zero destination with a non-zero mask is not a default",
			dump: rtMsg(rtmGet, 7, upGateway, dstGwMask, sa4(0, 0, 0, 0), sa4(192, 168, 1, 1), []byte{8, 0, 0, 0, 255, 0, 0, 0}),
		},
		{
			name: "a route that is not up is skipped",
			dump: rtMsg(rtmGet, 7, rtfGateway, dstGwMask, sa4(0, 0, 0, 0), sa4(192, 168, 1, 1), saMaskZero),
		},
		{
			name: "first unscoped default of a family wins",
			dump: cat(
				rtMsg(rtmGet, 7, upGateway, dstGwMask, sa4(0, 0, 0, 0), sa4(192, 168, 1, 1), saMaskZero),
				rtMsg(rtmGet, 22, upGateway, dstGwMask, sa4(0, 0, 0, 0), sa4(10, 8, 0, 1), saMaskZero),
			),
			v4: Route{Iface: "en0", Gateway: addr("192.168.1.1")},
		},
		{
			name: "other message versions are skipped",
			dump: func() []byte {
				m := rtMsg(rtmGet, 7, upGateway, dstGwMask, sa4(0, 0, 0, 0), sa4(192, 168, 1, 1), saMaskZero)
				m[2] = 4
				return m
			}(),
		},
		{name: "length past the buffer", dump: rtMsg(rtmGet, 7, upGateway, rtaDstBit, sa4(0, 0, 0, 0))[:50], wantErr: true},
		{name: "length zero", dump: make([]byte, 8), wantErr: true},
		{name: "message shorter than its header", dump: ifMsg(rtmGet, 7, 20), wantErr: true},
		{name: "sockaddr past the message", dump: rtMsg(rtmGet, 7, upGateway, rtaDstBit, []byte{16, afInet, 0, 0}), wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			v4, v6, err := routeDumpDefaults(tc.dump, names)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantErr {
				if !errors.Is(err, errMalformed) {
					t.Errorf("err = %v, want errMalformed", err)
				}
				return
			}
			if v4 != tc.v4 || v6 != tc.v6 {
				t.Errorf("routes = %+v %+v, want %+v %+v", v4, v6, tc.v4, tc.v6)
			}
		})
	}
}

func FuzzRouteSocket(f *testing.F) {
	f.Add(rtMsg(rtmGet, 7, upGateway, dstGwMask, sa4(0, 0, 0, 0), sa4(192, 168, 1, 1), saMaskZero))
	f.Add(rtMsg(rtmAdd, 7, upGateway, 0xff, sa6("::"), sa6("fe80::1"), saMaskZero, saLink(7)))
	f.Add(ifMsg(rtmIfinfo, 36, 112))
	f.Add([]byte{0xff, 0xff, 5, 1})
	f.Fuzz(func(t *testing.T, b []byte) {
		routeSocketHint(b)
		_, _, _ = routeDumpDefaults(b, map[int]string{7: "en0"})
	})
}
