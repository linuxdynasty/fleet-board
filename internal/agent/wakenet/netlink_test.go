package wakenet

import (
	"encoding/binary"
	"errors"
	"math"
	"testing"
)

// Netlink builders, from linux/netlink.h and linux/rtnetlink.h.

func nlAlign(n int) int { return (n + 3) &^ 3 }

// put16 and put32 write a length a test built; one that does not fit is a
// broken fixture.
func put16(b []byte, n int) {
	if n < 0 || n > math.MaxUint16 {
		panic("fixture length out of range")
	}
	binary.LittleEndian.PutUint16(b, uint16(n))
}

func put32(b []byte, n int) {
	if n < 0 || n > math.MaxUint32 {
		panic("fixture length out of range")
	}
	binary.LittleEndian.PutUint32(b, uint32(n))
}

func nlMsg(typ uint16, body []byte) []byte {
	n := nlHeaderLen + len(body)
	b := make([]byte, nlAlign(n))
	put32(b[0:], n)
	binary.LittleEndian.PutUint16(b[4:], typ)
	copy(b[nlHeaderLen:], body)
	return b
}

func nlAttr(typ uint16, data []byte) []byte {
	n := 4 + len(data)
	b := make([]byte, nlAlign(n))
	put16(b[0:], n)
	binary.LittleEndian.PutUint16(b[2:], typ)
	copy(b[4:], data)
	return b
}

func u32(v uint32) []byte { return binary.LittleEndian.AppendUint32(nil, v) }

func ifaddrmsg(family, prefixLen byte, index uint32) []byte {
	b := []byte{family, prefixLen, 0, 0}
	return append(b, u32(index)...)
}

func ifinfomsg(index, flags uint32) []byte {
	b := make([]byte, 16)
	binary.LittleEndian.PutUint32(b[4:], index)
	binary.LittleEndian.PutUint32(b[8:], flags)
	return b
}

// rtmsg is struct rtmsg followed by attributes.
func rtmsg(family, dstLen, table, typ byte, attrs ...[]byte) []byte {
	b := []byte{family, dstLen, 0, 0, table, 0, 0, typ, 0, 0, 0, 0}
	for _, a := range attrs {
		b = append(b, a...)
	}
	return b
}

func cat(parts ...[]byte) []byte {
	var b []byte
	for _, p := range parts {
		b = append(b, p...)
	}
	return b
}

const (
	afInet6Linux = 10
	tableMain    = 254
	rtnUnicast   = 1
)

func TestNetlinkHint(t *testing.T) {
	t.Parallel()
	defaultRoute := rtmsg(afInet, 0, tableMain, rtnUnicast)
	hostRoute := rtmsg(afInet, 32, tableMain, rtnUnicast)
	localTable := rtmsg(afInet, 0, 255, rtnUnicast)
	tests := []struct {
		name string
		b    []byte
		want bool
	}{
		{"address added (the omarchy IPv6 refresh)", nlMsg(nlNewAddr, ifaddrmsg(afInet6Linux, 64, 2)), true},
		{"address removed", nlMsg(nlDelAddr, ifaddrmsg(afInet, 24, 2)), true},
		{"link up", nlMsg(nlNewLink, ifinfomsg(2387, 0x41)), true},
		{"link removed", nlMsg(nlDelLink, ifinfomsg(2387, 0)), true},
		{"default route added", nlMsg(nlNewRoute, defaultRoute), true},
		{"default route removed", nlMsg(nlDelRoute, defaultRoute), true},
		{"host route is no hint", nlMsg(nlNewRoute, hostRoute), false},
		{"default route in the local table is no hint", nlMsg(nlNewRoute, localTable), false},
		{"neighbor message is no hint", nlMsg(28, make([]byte, 12)), false},
		{"route among others in one read", cat(nlMsg(28, make([]byte, 12)), nlMsg(nlNewRoute, defaultRoute)), true},
		{"empty read", nil, false},
		{"short header is a hint", []byte{1, 2, 3}, true},
		{"length past the buffer is a hint", nlMsg(nlNewRoute, defaultRoute)[:20], true},
		{"length under the header is a hint", []byte{4, 0, 0, 0, 24, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}, true},
		{"short route body is a hint", nlMsg(nlNewRoute, []byte{afInet}), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := netlinkHint(tc.b); got != tc.want {
				t.Errorf("netlinkHint = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestNetlinkDefaultRoutes(t *testing.T) {
	t.Parallel()
	names := map[int]string{2: "enp3s0", 3: "wlan0", 9: "tun0"}
	gw4 := func(a, b, c, d byte) []byte { return nlAttr(rtaGateway, []byte{a, b, c, d}) }
	gw6 := nlAttr(rtaGateway, addr("fe80::1").AsSlice())
	oif := func(i uint32) []byte { return nlAttr(rtaOif, u32(i)) }
	prio := func(p uint32) []byte { return nlAttr(rtaPriority, u32(p)) }
	route := func(body []byte) []byte { return nlMsg(nlNewRoute, body) }
	done := nlMsg(nlmsgDone, u32(0))

	nexthop := func(index uint32, attrs ...[]byte) []byte {
		hdr := make([]byte, nexthopLen)
		binary.LittleEndian.PutUint32(hdr[4:], index)
		b := cat(hdr, cat(attrs...))
		put16(b[0:], len(b))
		return b
	}

	tests := []struct {
		name    string
		dump    []byte
		v4, v6  Route
		wantErr bool
	}{
		{
			name: "wired IPv4 and IPv6 defaults",
			dump: cat(
				route(rtmsg(afInet, 24, tableMain, rtnUnicast, oif(2))),
				route(rtmsg(afInet, 0, tableMain, rtnUnicast, gw4(192, 168, 1, 1), oif(2), prio(100))),
				route(rtmsg(afInet6Linux, 0, tableMain, rtnUnicast, gw6, oif(2), prio(1024))),
				done),
			v4: Route{Iface: "enp3s0", Gateway: addr("192.168.1.1")},
			v6: Route{Iface: "enp3s0", Gateway: addr("fe80::1")},
		},
		{
			name: "lowest metric wins",
			dump: cat(
				route(rtmsg(afInet, 0, tableMain, rtnUnicast, gw4(192, 168, 1, 1), oif(2), prio(600))),
				route(rtmsg(afInet, 0, tableMain, rtnUnicast, gw4(10, 0, 0, 1), oif(3), prio(100))),
				done),
			v4: Route{Iface: "wlan0", Gateway: addr("10.0.0.1")},
		},
		{
			name: "VPN default with no gateway",
			dump: cat(route(rtmsg(afInet, 0, tableMain, rtnUnicast, oif(9))), done),
			v4:   Route{Iface: "tun0"},
		},
		{
			name: "table attribute overrides the header",
			dump: cat(
				route(rtmsg(afInet, 0, 252, rtnUnicast, nlAttr(rtaTable, u32(tableMain)), gw4(192, 168, 1, 1), oif(2))),
				route(rtmsg(afInet, 0, 252, rtnUnicast, nlAttr(rtaTable, u32(1000)), gw4(10, 9, 9, 9), oif(3), prio(0))),
				done),
			v4: Route{Iface: "enp3s0", Gateway: addr("192.168.1.1")},
		},
		{
			name: "multipath default takes its first hop",
			dump: cat(route(rtmsg(afInet, 0, tableMain, rtnUnicast,
				nlAttr(rtaMultipath, cat(nexthop(3, gw4(10, 0, 0, 1)), nexthop(2, gw4(192, 168, 1, 1)))))), done),
			v4: Route{Iface: "wlan0", Gateway: addr("10.0.0.1")},
		},
		{
			name: "unreachable and other tables are skipped",
			dump: cat(
				route(rtmsg(afInet6Linux, 0, tableMain, 7, oif(1))),
				route(rtmsg(afInet, 0, 255, rtnUnicast, oif(2))),
				done),
		},
		{
			name: "unknown interface index keeps the route, with no name",
			dump: cat(route(rtmsg(afInet, 0, tableMain, rtnUnicast, gw4(192, 168, 1, 1), oif(77))), done),
			v4:   Route{Gateway: addr("192.168.1.1")},
		},
		{name: "error message", dump: nlMsg(nlmsgError, u32(0xfffffff3)), wantErr: true},
		{name: "length past the buffer", dump: route(rtmsg(afInet, 0, tableMain, rtnUnicast, oif(2)))[:20], wantErr: true},
		{name: "short route body", dump: route([]byte{afInet, 0}), wantErr: true},
		{name: "attribute past its message", dump: route(append(rtmsg(afInet, 0, tableMain, rtnUnicast), 200, 0, 4, 0)), wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			v4, v6, err := netlinkDefaultRoutes(tc.dump, names)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantErr {
				if !errors.Is(err, errMalformed) && !errors.Is(err, errDump) {
					t.Errorf("err = %v, want errMalformed or errDump", err)
				}
				return
			}
			if v4 != tc.v4 || v6 != tc.v6 {
				t.Errorf("routes = %+v %+v, want %+v %+v", v4, v6, tc.v4, tc.v6)
			}
		})
	}
}

func FuzzNetlink(f *testing.F) {
	f.Add(nlMsg(nlNewAddr, ifaddrmsg(afInet6Linux, 64, 2)))
	f.Add(nlMsg(nlNewRoute, rtmsg(afInet, 0, tableMain, rtnUnicast, nlAttr(rtaOif, u32(2)))))
	f.Add(nlMsg(nlNewRoute, rtmsg(afInet, 0, tableMain, rtnUnicast, nlAttr(rtaMultipath, make([]byte, 8)))))
	f.Add([]byte{0xff, 0xff, 0xff, 0xff})
	f.Fuzz(func(t *testing.T, b []byte) {
		netlinkHint(b)
		_, _, _ = netlinkDefaultRoutes(b, map[int]string{2: "eth0"})
	})
}
