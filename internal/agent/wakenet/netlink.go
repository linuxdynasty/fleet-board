package wakenet

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
)

// Linux rtnetlink ABI (linux/netlink.h, linux/rtnetlink.h). The parsers are
// pure byte code with no build tag so their tests run on any OS;
// consts_linux_test.go checks the numbers against x/sys/unix.
const (
	nlHeaderLen  = 16 // struct nlmsghdr
	rtmsgLen     = 12 // struct rtmsg
	nexthopLen   = 8  // struct rtnexthop
	nlmsgError   = 2
	nlmsgDone    = 3
	nlNewLink    = 16
	nlDelLink    = 17
	nlNewAddr    = 20
	nlDelAddr    = 21
	nlNewRoute   = 24
	nlDelRoute   = 25
	rtaGateway   = 5
	rtaOif       = 4
	rtaPriority  = 6
	rtaMultipath = 9
	rtaTable     = 15
	rtTableMain  = 254
	rtnUnicastT  = 1
	afInet       = 2  // the same on Linux and macOS
	afInet6Nl    = 10 // Linux AF_INET6
)

var (
	errMalformed = errors.New("malformed routing message")
	errDump      = errors.New("routing table dump refused")
)

// nlMessages calls fn for each netlink message in b with its type and body.
// It returns errMalformed when a length is impossible; fn returning false stops.
func nlMessages(b []byte, fn func(typ uint16, body []byte) bool) error {
	for len(b) > 0 {
		if len(b) < nlHeaderLen {
			return fmt.Errorf("%w: %d bytes left, header is %d", errMalformed, len(b), nlHeaderLen)
		}
		n := binary.LittleEndian.Uint32(b[0:])
		if n < nlHeaderLen || uint64(n) > uint64(len(b)) {
			return fmt.Errorf("%w: message length %d of %d bytes", errMalformed, n, len(b))
		}
		if !fn(binary.LittleEndian.Uint16(b[4:]), b[nlHeaderLen:n]) {
			return nil
		}
		next := min((uint64(n)+3)&^3, uint64(len(b)))
		b = b[next:]
	}
	return nil
}

// netlinkHint reports whether one push read holds a message that could change
// the reading: a link or address message, or a main-table default route. A
// read it cannot parse counts as a hint: a hint only starts a read.
func netlinkHint(b []byte) bool {
	hint := false
	err := nlMessages(b, func(typ uint16, body []byte) bool {
		switch typ {
		case nlNewLink, nlDelLink, nlNewAddr, nlDelAddr:
			hint = true
		case nlNewRoute, nlDelRoute:
			if len(body) < rtmsgLen {
				hint = true
			} else if r, err := parseRtmsg(body); err != nil || r.isMainDefault() {
				hint = true
			}
		}
		return !hint
	})
	return hint || err != nil
}

// netlinkDefaultRoutes reads an RTM_GETROUTE dump and returns the main-table
// unicast default route of each family with the lowest metric.
func netlinkDefaultRoutes(b []byte, names map[int]string) (v4, v6 Route, err error) {
	var best4, best6 *nlRoute
	var perr error
	err = nlMessages(b, func(typ uint16, body []byte) bool {
		switch typ {
		case nlmsgDone:
			return false
		case nlmsgError:
			perr = fmt.Errorf("%w: netlink error message", errDump)
			return false
		case nlNewRoute:
		default:
			return true
		}
		r, err := parseRtmsg(body)
		if err != nil {
			perr = err
			return false
		}
		if !r.isMainDefault() {
			return true
		}
		switch r.family {
		case afInet:
			if best4 == nil || r.priority < best4.priority {
				best4 = &r
			}
		case afInet6Nl:
			if best6 == nil || r.priority < best6.priority {
				best6 = &r
			}
		}
		return true
	})
	if err == nil {
		err = perr
	}
	if err != nil {
		return Route{}, Route{}, err
	}
	return best4.route(names), best6.route(names), nil
}

// nlRoute is the part of one route message the reading needs.
type nlRoute struct {
	family, dstLen, typ byte
	table, priority     uint32
	oif                 uint32
	gateway             netip.Addr
}

func (r nlRoute) isMainDefault() bool {
	return r.dstLen == 0 && r.table == rtTableMain && r.typ == rtnUnicastT
}

func (r *nlRoute) route(names map[int]string) Route {
	if r == nil {
		return Route{}
	}
	return Route{Iface: names[int(r.oif)], Gateway: r.gateway}
}

// parseRtmsg reads struct rtmsg and the attributes the reading needs. A
// multipath route takes its first next hop.
func parseRtmsg(body []byte) (nlRoute, error) {
	if len(body) < rtmsgLen {
		return nlRoute{}, fmt.Errorf("%w: route body %d bytes", errMalformed, len(body))
	}
	r := nlRoute{family: body[0], dstLen: body[1], table: uint32(body[4]), typ: body[7]}
	err := nlAttrs(body[rtmsgLen:], func(typ uint16, data []byte) error {
		switch typ {
		case rtaTable:
			if len(data) >= 4 {
				r.table = binary.LittleEndian.Uint32(data)
			}
		case rtaPriority:
			if len(data) >= 4 {
				r.priority = binary.LittleEndian.Uint32(data)
			}
		case rtaOif:
			if len(data) >= 4 {
				r.oif = binary.LittleEndian.Uint32(data)
			}
		case rtaGateway:
			r.gateway, _ = netip.AddrFromSlice(data)
		case rtaMultipath:
			if r.oif == 0 && len(data) >= nexthopLen {
				return r.firstHop(data)
			}
		}
		return nil
	})
	return r, err
}

// firstHop reads the first struct rtnexthop of a multipath attribute.
func (r *nlRoute) firstHop(data []byte) error {
	n := int(binary.LittleEndian.Uint16(data))
	if n < nexthopLen || n > len(data) {
		return fmt.Errorf("%w: next hop length %d of %d", errMalformed, n, len(data))
	}
	r.oif = binary.LittleEndian.Uint32(data[4:])
	return nlAttrs(data[nexthopLen:n], func(typ uint16, d []byte) error {
		if typ == rtaGateway {
			r.gateway, _ = netip.AddrFromSlice(d)
		}
		return nil
	})
}

// nlAttrs calls fn for each struct rtattr in b.
func nlAttrs(b []byte, fn func(typ uint16, data []byte) error) error {
	for len(b) >= 4 {
		n := int(binary.LittleEndian.Uint16(b))
		if n < 4 || n > len(b) {
			return fmt.Errorf("%w: attribute length %d of %d", errMalformed, n, len(b))
		}
		if err := fn(binary.LittleEndian.Uint16(b[2:]), b[4:n]); err != nil {
			return err
		}
		b = b[min((n+3)&^3, len(b)):]
	}
	return nil
}
