package wakenet

import (
	"encoding/binary"
	"fmt"
	"net/netip"
)

// macOS PF_ROUTE ABI (net/route.h, net/if.h, sys/socket.h). Pure byte code
// with no build tag so the tests run on any OS; consts_darwin_test.go checks
// the numbers against x/sys/unix.
const (
	rtMsghdrLen    = 92 // struct rt_msghdr, the same on arm64 and amd64
	rtmVersion     = 5
	rtmAdd         = 0x1
	rtmDelete      = 0x2
	rtmChange      = 0x3
	rtmGet         = 0x4
	rtmMiss        = 0x7
	rtmNewAddr     = 0xc
	rtmDelAddr     = 0xd
	rtmIfinfo      = 0xe
	rtmIfinfo2     = 0x12
	rtaDstBit      = 0x1
	rtaGatewayBit  = 0x2
	rtaNetmaskBit  = 0x4
	rtaxMax        = 8
	rtfUp          = 0x1
	rtfGateway     = 0x2
	rtfHost        = 0x4
	rtfIfscope     = 0x1000000
	afInet6Darwin  = 30
	afLinkDarwin   = 18
	sockaddrInLen  = 8  // through sin_addr
	sockaddrIn6Len = 24 // through sin6_addr
)

// rtMessages calls fn for each routing message in b with its type and bytes.
// It returns errMalformed when a length is impossible; fn returning false stops.
func rtMessages(b []byte, fn func(typ byte, m []byte) bool) error {
	for len(b) > 0 {
		if len(b) < 4 {
			return fmt.Errorf("%w: %d bytes left", errMalformed, len(b))
		}
		n := int(binary.LittleEndian.Uint16(b))
		if n < 4 || n > len(b) {
			return fmt.Errorf("%w: message length %d of %d bytes", errMalformed, n, len(b))
		}
		if !fn(b[3], b[:n]) {
			return nil
		}
		b = b[n:]
	}
	return nil
}

// routeSocketHint reports whether one push read holds a message that could
// change the reading: interface info, an address added or removed, or a
// default route added, removed or changed. Lookup traffic (RTM_MISS, RTM_GET)
// is not. A read it cannot parse counts as a hint: a hint only starts a read.
func routeSocketHint(b []byte) bool {
	hint := false
	err := rtMessages(b, func(typ byte, m []byte) bool {
		switch typ {
		case rtmIfinfo, rtmIfinfo2, rtmNewAddr, rtmDelAddr:
			hint = true
		case rtmAdd, rtmDelete, rtmChange:
			r, err := parseRtMsg(m)
			hint = err != nil || r.isDefault()
		}
		return !hint
	})
	return hint || err != nil
}

// routeDumpDefaults reads a net.route NET_RT_DUMP sysctl and returns, for each
// family, the first default route that is up and not scoped to one interface
// (RTF_IFSCOPE): the route macOS uses when a socket names no interface.
func routeDumpDefaults(b []byte, names map[int]string) (v4, v6 Route, err error) {
	var have4, have6 bool
	var perr error
	err = rtMessages(b, func(_ byte, m []byte) bool {
		if m[2] != rtmVersion {
			return true
		}
		r, err := parseRtMsg(m)
		if err != nil {
			perr = err
			return false
		}
		if !r.isDefault() || r.flags&rtfUp == 0 || r.flags&rtfIfscope != 0 {
			return true
		}
		rt := Route{Iface: names[int(r.index)], Gateway: r.gateway}
		switch {
		case r.family == afInet && !have4:
			v4, have4 = rt, true
		case r.family == afInet6Darwin && !have6:
			v6, have6 = rt, true
		}
		return true
	})
	if err == nil {
		err = perr
	}
	if err != nil {
		return Route{}, Route{}, err
	}
	return v4, v6, nil
}

// rtRoute is the part of one rt_msghdr message the reading needs.
type rtRoute struct {
	index      uint16
	flags      uint32
	family     byte // of the destination
	dstZero    bool
	maskZero   bool
	hasDst     bool
	gateway    netip.Addr
	hasGateway bool
}

func (r rtRoute) isDefault() bool {
	return r.hasDst && r.dstZero && r.maskZero && r.flags&rtfHost == 0
}

// parseRtMsg reads struct rt_msghdr and its destination, gateway and netmask
// sockaddrs, which follow in RTA_* bit order, each padded to 4 bytes.
func parseRtMsg(m []byte) (rtRoute, error) {
	if len(m) < rtMsghdrLen {
		return rtRoute{}, fmt.Errorf("%w: route message %d bytes, header is %d", errMalformed, len(m), rtMsghdrLen)
	}
	r := rtRoute{
		index:    binary.LittleEndian.Uint16(m[4:]),
		flags:    binary.LittleEndian.Uint32(m[8:]),
		maskZero: true,
	}
	addrs := binary.LittleEndian.Uint32(m[12:])
	b := m[rtMsghdrLen:]
	for bit := range rtaxMax {
		if addrs&(1<<bit) == 0 {
			continue
		}
		if len(b) < 1 {
			return rtRoute{}, fmt.Errorf("%w: sockaddr %d missing", errMalformed, bit)
		}
		saLen := int(b[0])
		size := 4
		if saLen > 0 {
			size = (saLen + 3) &^ 3
		}
		if saLen > len(b) || size > len(b) {
			return rtRoute{}, fmt.Errorf("%w: sockaddr %d length %d of %d", errMalformed, bit, saLen, len(b))
		}
		sa := b[:saLen]
		switch 1 << bit {
		case rtaDstBit:
			r.hasDst = len(sa) >= 2
			if r.hasDst {
				r.family = sa[1]
				ip := saIP(sa)
				r.dstZero = ip.IsValid() && ip.IsUnspecified()
			}
		case rtaGatewayBit:
			r.gateway, r.hasGateway = saIP(sa), true
		case rtaNetmaskBit:
			r.maskZero = maskZero(sa, r.family)
		}
		b = b[size:]
	}
	return r, nil
}

// saIP returns the address of a sockaddr_in or sockaddr_in6, with the
// interface index macOS embeds in a link-local address cleared. Any other
// family (a sockaddr_dl gateway) gives the zero Addr.
func saIP(sa []byte) netip.Addr {
	if len(sa) < 2 {
		return netip.Addr{}
	}
	switch sa[1] {
	case afInet:
		if len(sa) >= sockaddrInLen {
			return netip.AddrFrom4([4]byte(sa[4:8]))
		}
	case afInet6Darwin:
		if len(sa) >= sockaddrIn6Len {
			a := [16]byte(sa[8:24])
			if a[0] == 0xfe && a[1]&0xc0 == 0x80 {
				a[2], a[3] = 0, 0
			}
			return netip.AddrFrom16(a)
		}
	}
	return netip.Addr{}
}

// maskZero reports whether a netmask sockaddr, which macOS truncates after its
// last non-zero byte, is all zero for the destination's family.
func maskZero(sa []byte, family byte) bool {
	start := 4
	if family == afInet6Darwin {
		start = 8
	}
	for i := start; i < len(sa); i++ {
		if sa[i] != 0 {
			return false
		}
	}
	return true
}
